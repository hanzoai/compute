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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/orm/relational"
)

// A provider table an older schema left with key columns, and rotation keys
// carrying secrets, holds none of it once its engine is opened: the columns are
// dropped, the keys are rewritten without their secrets, and the file itself no
// longer contains the values.
func TestAnOldProviderTableForgetsItsKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.db")
	engine, err := relational.NewEngine("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Exec(`CREATE TABLE provider (owner TEXT NOT NULL, name TEXT NOT NULL,
		client_id TEXT, client_secret TEXT, type TEXT, category TEXT, region TEXT, state TEXT, keys TEXT,
		PRIMARY KEY (owner, name))`); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Exec(`INSERT INTO provider (owner, name, client_id, client_secret, type, category, region, state, keys)
		VALUES ('acme', 'hz', 'AKIAOLDKEYID00000000', 'dop_v1_the_stored_secret', 'Hetzner', 'Cloud', 'fsn1', 'Active',
		'[{"name":"a","keyId":"AKIAROTATION0000000","secret":"tok-rotation-secret","region":"nbg1"}]')`); err != nil {
		t.Fatal(err)
	}
	if err := engine.Sync2(perOrgModels()...); err != nil {
		t.Fatal(err)
	}

	if err := forgetProviderKeys(engine); err != nil {
		t.Fatalf("forgetProviderKeys: %v", err)
	}
	metas, err := engine.DBMetas()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range metas {
		if table.Name != "provider" {
			continue
		}
		for _, col := range keyColumns {
			if table.GetColumn(col) != nil {
				t.Errorf("provider.%s still exists", col)
			}
		}
	}
	var rows []*Provider
	if err := engine.Find(&rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if p := rows[0]; p.Name != "hz" || p.Region != "fsn1" || len(p.Keys) != 1 || p.Keys[0].Name != "a" || p.Keys[0].Region != "nbg1" {
		t.Fatalf("the row lost more than its keys: %+v", p)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"dop_v1_the_stored_secret", "tok-rotation-secret", "AKIAOLDKEYID00000000", "AKIAROTATION0000000"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q is still in the database file", secret)
		}
	}
	// Once clean, it is a no-op.
	if err := forgetProviderKeys(engine); err != nil {
		t.Fatalf("a second pass: %v", err)
	}
}
