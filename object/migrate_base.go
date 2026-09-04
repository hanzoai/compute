// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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
	"reflect"

	"github.com/hanzoai/orm/relational"
)

// MigrationReport records what MigratePostgresToBase copied for one table.
type MigrationReport struct {
	Table       string
	SourceRows  int
	WrittenRows int
	Orgs        int // distinct owner DBs the rows fanned into
}

// MigratePostgresToBase copies the per-tenant compute tables from the shared
// Postgres engine into per-org Base SQLite files, routing each row to
// DBPath(owner). It iterates perOrgModels() -- the SAME registry the Base schema
// sync uses -- so it can never drift from what an org DB actually holds. The
// shared tables (Plan catalog, MeterLease lease) are deliberately NOT migrated:
// they stay on the shared Postgres coordination engine under Base mode (see
// LLM.md, Base backend: shared vs per-org).
//
// It NEVER runs at boot: an operator invokes it explicitly during the cutover
// window (e.g. a `compute migrate` subcommand). Rows are grouped by their Owner
// field; the rare row with an empty Owner (e.g. a Record whose Organization was
// unset) routes to the _global sentinel DB. This mirrors hanzo/cloud's
// introspective migration/pg_to_sqlite.go, but is schema-aware because compute
// owns its models rather than a drifted upstream schema.
func MigratePostgresToBase(src *relational.Engine, dst *baseStore) ([]MigrationReport, error) {
	if src == nil {
		return nil, fmt.Errorf("compute: migrate: nil source engine")
	}
	if dst == nil {
		return nil, fmt.Errorf("compute: migrate: nil base store")
	}

	reports := make([]MigrationReport, 0, len(perOrgModels()))
	for _, model := range perOrgModels() {
		rep, err := migrateModel(src, dst, model)
		if err != nil {
			return reports, err
		}
		reports = append(reports, rep)
	}
	return reports, nil
}

func migrateModel(src *relational.Engine, dst *baseStore, model interface{}) (MigrationReport, error) {
	elemType := reflect.TypeOf(model).Elem()
	rep := MigrationReport{Table: src.TableName(model, true)}

	// A []*Model slice to receive every source row.
	rowsPtr := reflect.New(reflect.SliceOf(reflect.PtrTo(elemType)))
	if err := src.Find(rowsPtr.Interface()); err != nil {
		return rep, fmt.Errorf("compute: migrate: read %s: %w", rep.Table, err)
	}
	rows := rowsPtr.Elem()
	rep.SourceRows = rows.Len()

	seen := map[string]struct{}{}
	for i := 0; i < rows.Len(); i++ {
		owner := ownerOf(rows.Index(i))
		engine, err := dst.EngineFor(owner)
		if err != nil {
			return rep, err
		}
		if _, err := engine.Insert(rows.Index(i).Interface()); err != nil {
			return rep, fmt.Errorf("compute: migrate: write %s owner=%q: %w", rep.Table, owner, err)
		}
		rep.WrittenRows++
		if owner == "" {
			owner = "_global"
		}
		seen[owner] = struct{}{}
	}
	rep.Orgs = len(seen)
	return rep, nil
}

// ownerOf extracts the row's Owner field, or "" when the model has none (which
// routes to the _global DB). elem is a reflect.Value of a *Model.
func ownerOf(elem reflect.Value) string {
	f := elem.Elem().FieldByName("Owner")
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}
