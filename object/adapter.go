// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"fmt"
	"runtime"

	_ "github.com/go-sql-driver/mysql"
	"github.com/hanzoai/visor/conf"
	"github.com/hanzoai/visor/util"
	_ "github.com/lib/pq"

	"github.com/hanzoai/orm/relational"
)

var adapter *Adapter

// InitConfig is retained as the config+store bootstrap entry point. Config now
// loads lazily on first read (conf package), so this simply opens the store.
func InitConfig() {
	InitAdapter()
}

func InitAdapter() {
	// SQLite/Base is the default substrate (platform rule: SQLite for everything; no
	// Postgres unless a multi-instance deployment opts in with storageBackend=postgres).
	// In Base mode NOTHING dials Postgres — the per-org + `_global` SQLite DBs are the
	// whole store, so a cluster with no SQL service boots clean.
	if ConfiguredBackend() == BackendBase {
		bs, err := newBaseStore(dataRoot())
		if err != nil {
			panic(fmt.Errorf("visor: init base store: %w", err))
		}
		store = bs
		// Legacy `adapter.engine` references (pre-seam call sites, createDatabase)
		// resolve to the `_global` SQLite coord engine — never nil, never Postgres.
		adapter = &Adapter{driverName: "sqlite", engine: bs.Shared()}
		return
	}
	// Postgres: multi-instance production only (opt-in). The historical shared engine.
	adapter = NewAdapter(conf.GetConfigString("driverName"), conf.GetConfigDataSourceName())
	InitStore()
}

// Adapter represents the database adapter for policy storage.
type Adapter struct {
	driverName     string
	dataSourceName string
	engine         *relational.Engine
}

// finalizer is the destructor for Adapter.
func finalizer(a *Adapter) {
	err := a.engine.Close()
	if err != nil {
		panic(err)
	}
}

// NewAdapter is the constructor for Adapter.
func NewAdapter(driverName string, dataSourceName string) *Adapter {
	a := &Adapter{}
	a.driverName = driverName
	a.dataSourceName = dataSourceName

	// Open the DB, create it if not existed.
	a.open()

	// Call the destructor when the object is released.
	runtime.SetFinalizer(a, finalizer)

	return a
}

func (a *Adapter) createDatabase() error {
	if a.driverName == "postgres" {
		// PostgreSQL: connect without dbname to create the database
		engine, err := relational.NewEngine(a.driverName, a.dataSourceName)
		if err != nil {
			return err
		}
		defer engine.Close()

		dbName := conf.GetConfigString("dbName")
		// Check if DB exists; create if not (PostgreSQL syntax)
		var count int64
		_, err = engine.SQL("SELECT COUNT(*) FROM pg_database WHERE datname = ?", dbName).Get(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			_, err = engine.Exec(fmt.Sprintf("CREATE DATABASE %s", dbName))
			if err != nil {
				return err
			}
		}
		return nil
	}

	// MySQL fallback
	engine, err := relational.NewEngine(a.driverName, a.dataSourceName)
	if err != nil {
		return err
	}
	defer engine.Close()

	_, err = engine.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s default charset utf8 COLLATE utf8_general_ci", conf.GetConfigString("dbName")))
	return err
}

func (a *Adapter) open() {
	if err := a.createDatabase(); err != nil {
		panic(err)
	}

	var dsn string
	if a.driverName == "postgres" {
		// For PostgreSQL, the dataSourceName already contains the dbname
		dsn = a.dataSourceName
	} else {
		// For MySQL, append dbName
		dsn = a.dataSourceName + conf.GetConfigString("dbName")
	}

	engine, err := relational.NewEngine(a.driverName, dsn)
	if err != nil {
		panic(err)
	}

	a.engine = engine
	a.createTable()
}

func (a *Adapter) close() {
	a.engine.Close()
	a.engine = nil
}

func (a *Adapter) createTable() {
	// models() is the single table registry, shared with the Base backend and
	// the Postgres->Base migration. Same DDL, one source of truth.
	for _, m := range models() {
		if err := a.engine.Sync2(m); err != nil {
			panic(err)
		}
	}
}

func GetSession(owner string, offset, limit int, field, value, sortField, sortOrder string) *relational.Session {
	session := mustEngineFor(owner).Prepare()
	if offset != -1 && limit != -1 {
		session.Limit(limit, offset)
	}
	if owner != "" {
		session = session.And("owner=?", owner)
	}
	if field != "" && value != "" {
		if util.FilterField(field) {
			session = session.And(fmt.Sprintf("%s like ?", util.SnakeString(field)), fmt.Sprintf("%%%s%%", value))
		}
	}
	// The sort column is whitelisted exactly like the filter column above, and
	// for the same reason: both are caller-supplied and both land in an
	// IDENTIFIER position, where a bound parameter cannot protect them.
	// SnakeString does not sanitize — it only lowercases and inserts "_" before
	// capitals — so an all-lowercase payload reaches ORDER BY unchanged, and
	// "/**/" stands in for the spaces. Every paginated list endpoint takes this
	// parameter.
	//
	// A rejected value falls back to the default rather than erroring: sort
	// order is presentation, and the UI sends Ant Design dataIndex names
	// ("createdTime", "displayName"), which are alphanumeric and pass. The
	// default is a literal, so it is set AFTER the check and never has to
	// satisfy it (it contains "_", which the whitelist deliberately excludes).
	if sortField == "" || sortOrder == "" || !util.FilterField(sortField) {
		sortField = "created_time"
	}
	if sortOrder == "ascend" {
		session = session.Asc(util.SnakeString(sortField))
	} else {
		session = session.Desc(util.SnakeString(sortField))
	}
	return session
}
