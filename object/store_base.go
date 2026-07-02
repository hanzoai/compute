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
	"os"
	"path/filepath"
	"sync"

	// modernc registers the CGO-free "sqlite" database/sql driver; xorm maps
	// the "sqlite" driver name onto its sqlite3 dialect (see
	// xorm.io/xorm/dialects/dialect.go). This is the same driver hanzoai/base
	// uses, so visor and base share one SQLite engine under CGO_ENABLED=0.
	_ "modernc.org/sqlite"
	"xorm.io/xorm"
)

// sqlitePragmas mirrors the durability profile hanzoai/base applies to its
// per-tenant SQLite files: a generous busy timeout, WAL, NORMAL sync, and
// foreign keys on. modernc reads these from the DSN query string; xorm passes
// the full DSN through to the driver.
const sqlitePragmas = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"

// baseStore serves each owner from its own SQLite file under a data root -- one
// *xorm.Engine per org, opened lazily and cached for the process lifetime. This
// is the per-tenant Base substrate (HIP-0302). The same xorm models and queries
// run unchanged against it; only the resolved engine differs from Postgres.
//
// Slice-1 scope: local per-org files only. Object-storage replication (the
// base/store S3 hydration path, or hanzo/cloud's org.Replicator) is a follow-up
// slice -- see the migration plan / LLM.md.
type baseStore struct {
	root string

	mu      sync.Mutex
	engines map[string]*xorm.Engine
}

func newBaseStore(root string) (*baseStore, error) {
	if root == "" {
		return nil, fmt.Errorf("visor: base store data root is empty")
	}
	return &baseStore{root: root, engines: map[string]*xorm.Engine{}}, nil
}

// EngineFor returns the org's SQLite engine, opening and schema-syncing it on
// first use. Concurrency-safe; each org DB is opened exactly once.
func (s *baseStore) EngineFor(owner string) (*xorm.Engine, error) {
	if owner == "" {
		owner = "_global"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.engines[owner]; ok {
		return e, nil
	}

	path := DBPath(s.root, owner)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("visor: base store mkdir %s: %w", filepath.Dir(path), err)
	}

	engine, err := xorm.NewEngine("sqlite", path+sqlitePragmas)
	if err != nil {
		return nil, fmt.Errorf("visor: base store open %s: %w", path, err)
	}
	if err := engine.Sync2(models()...); err != nil {
		_ = engine.Close()
		return nil, fmt.Errorf("visor: base store sync %s: %w", path, err)
	}

	s.engines[owner] = engine
	return engine, nil
}

// Close closes every open per-org engine, returning the first error.
func (s *baseStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for owner, e := range s.engines {
		if err := e.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("visor: base store close %s: %w", owner, err)
		}
		delete(s.engines, owner)
	}
	return firstErr
}
