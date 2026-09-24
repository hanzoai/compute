// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

	"github.com/hanzoai/orm/relational"
	"github.com/hanzoai/orm/relational/schemas"
)

// keyColumns are the provider columns that held a cloud key: the account's key
// id and its secret. Provider has neither field; forgetProviderKeys drops the
// columns wherever an older schema left them.
var keyColumns = []string{"client_secret", "client_id"}

// forgetProviderKeys removes every cloud key an engine's provider table holds:
// it drops the key columns, rewrites each row's rotation keys through a
// ProviderKey that has no secret, and — on SQLite — vacuums, so the dropped
// values are gone from the file and not left in its free pages. It runs each
// time an engine is opened, and does nothing once the table is clean.
func forgetProviderKeys(engine *relational.Engine) error {
	table := engine.TableName(&Provider{})
	metas, err := engine.DBMetas()
	if err != nil {
		return fmt.Errorf("visor: read the %s schema: %w", table, err)
	}
	var dropped bool
	for _, t := range metas {
		if t.Name != table {
			continue
		}
		for _, col := range keyColumns {
			if t.GetColumn(col) == nil {
				continue
			}
			if _, err := engine.Exec(fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", engine.Quote(table), engine.Quote(col))); err != nil {
				return fmt.Errorf("visor: drop %s.%s: %w", table, col, err)
			}
			dropped = true
		}
	}

	var rows []*Provider
	if err := engine.Find(&rows); err != nil {
		return fmt.Errorf("visor: read %s: %w", table, err)
	}
	for _, p := range rows {
		if len(p.Keys) == 0 {
			continue
		}
		if _, err := engine.ID(schemas.PK{p.Owner, p.Name}).Cols("keys").Update(p); err != nil {
			return fmt.Errorf("visor: rewrite the keys of %s/%s: %w", p.Owner, p.Name, err)
		}
		dropped = true
	}

	if dropped && engine.DriverName() == "sqlite" {
		if _, err := engine.Exec("VACUUM"); err != nil {
			return fmt.Errorf("visor: vacuum after dropping keys: %w", err)
		}
	}
	return nil
}
