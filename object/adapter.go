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

	"github.com/beego/beego"
	_ "github.com/go-sql-driver/mysql"
	"github.com/hanzoai/visor/conf"
	"github.com/hanzoai/visor/util"
	_ "github.com/lib/pq"
	"xorm.io/xorm"
)

var adapter *Adapter

func InitConfig() {
	err := beego.LoadAppConfig("ini", "../conf/app.conf")
	if err != nil {
		panic(err)
	}

	InitAdapter()
}

func InitAdapter() {
	adapter = NewAdapter(conf.GetConfigString("driverName"), conf.GetConfigDataSourceName())
}

// Adapter represents the database adapter for policy storage.
type Adapter struct {
	driverName     string
	dataSourceName string
	engine         *xorm.Engine
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
		engine, err := xorm.NewEngine(a.driverName, a.dataSourceName)
		if err != nil {
			return err
		}
		defer engine.Close()

		dbName := beego.AppConfig.String("dbName")
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
	engine, err := xorm.NewEngine(a.driverName, a.dataSourceName)
	if err != nil {
		return err
	}
	defer engine.Close()

	_, err = engine.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s default charset utf8 COLLATE utf8_general_ci", beego.AppConfig.String("dbName")))
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
		dsn = a.dataSourceName + beego.AppConfig.String("dbName")
	}

	engine, err := xorm.NewEngine(a.driverName, dsn)
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
	err := a.engine.Sync2(new(Asset))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(Provider))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(Machine))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(Record))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(Session))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(NodePool))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(Plan))
	if err != nil {
		panic(err)
	}

	err = a.engine.Sync2(new(Volume))
	if err != nil {
		panic(err)
	}
}

func GetSession(owner string, offset, limit int, field, value, sortField, sortOrder string) *xorm.Session {
	session := adapter.engine.Prepare()
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
	if sortField == "" || sortOrder == "" {
		sortField = "created_time"
	}
	if sortOrder == "ascend" {
		session = session.Asc(util.SnakeString(sortField))
	} else {
		session = session.Desc(util.SnakeString(sortField))
	}
	return session
}
