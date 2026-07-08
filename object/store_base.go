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

// baseStore serves each owner's per-tenant tables from its own SQLite file under
// a data root -- one *xorm.Engine per org, opened lazily and cached for the
// process lifetime. It also holds coord, the shared Postgres engine that serves
// the cross-pod shared tables (Plan catalog, MeterLease lease) which cannot live
// in per-org SQLite. The same xorm models and queries run unchanged against
// either; only the resolved engine differs from Postgres.
//
// Durability/HA scope: per-org files are local to the pod's data root. In
// production dataRoot is a persistent volume, so an org DB survives a pod
// restart. Serving one org from more than one pod concurrently requires
// WAL->object-storage replication with single-writer-per-org coordination; that
// is a separate slice, and until it lands Base mode is safe under replicas:1 or
// org-sharded routing. See LLM.md (Base backend: durability & replication) for
// the exact integration point and the interim operational constraint.
type baseStore struct {
	root  string
	coord *xorm.Engine // the _global SQLite engine for the cross-pod shared tables

	repl *replicator // HA object-store binding (nil = local-only; see store_replica.go)

	mu      sync.Mutex
	engines map[string]*xorm.Engine
}

// newBaseStore opens the per-org SQLite substrate. The cross-pod shared tables
// (Plan catalog + MeterLease lease) live in the ONE `_global` SQLite DB — no
// Postgres anywhere (house rule: SQLite/Base for everything). This is a valid
// single coordination store because visor runs replicas=1: one pod == one writer,
// so the MeterLease insert-once lease cannot double-fire. Scaling visor out later
// needs a real cluster coordinator (WAL→object-storage single-writer election,
// the staged replication note above) — until then Base mode is single-replica.
func newBaseStore(root string) (*baseStore, error) {
	if root == "" {
		return nil, fmt.Errorf("visor: base store data root is empty")
	}
	// HA object-store binding (opt-in via REPLICA_STORE; nil = local-only). Never
	// fatal — a bad/unreachable store degrades to local-only inside newReplicator.
	repl := newReplicator(root)

	coordPath := DBPath(root, "_global")
	if err := os.MkdirAll(filepath.Dir(coordPath), 0o700); err != nil {
		return nil, fmt.Errorf("visor: base store coord dir %s: %w", filepath.Dir(coordPath), err)
	}
	// Hydrate the shared (_global) DB from the object store BEFORE opening it.
	if err := repl.hydrate("_global", coordPath); err != nil {
		return nil, fmt.Errorf("visor: base store hydrate coord: %w", err)
	}
	coord, err := xorm.NewEngine("sqlite", coordPath+sqlitePragmas)
	if err != nil {
		return nil, fmt.Errorf("visor: base store open coord %s: %w", coordPath, err)
	}
	// The shared tables live only here (never synced into a per-org DB).
	if err := coord.Sync2(sharedModels()...); err != nil {
		_ = coord.Close()
		return nil, fmt.Errorf("visor: base store sync coord: %w", err)
	}
	bs := &baseStore{root: root, coord: coord, repl: repl, engines: map[string]*xorm.Engine{}}
	repl.ship("_global", coordPath) // back up the shared DB on an interval
	return bs, nil
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
	// Hydrate this org's DB from the object store BEFORE opening it (HA: a fresh
	// pod pulls the org's last committed state; a brand-new org starts empty).
	if err := s.repl.hydrate(owner, path); err != nil {
		return nil, fmt.Errorf("visor: base store hydrate %s: %w", owner, err)
	}

	engine, err := xorm.NewEngine("sqlite", path+sqlitePragmas)
	if err != nil {
		return nil, fmt.Errorf("visor: base store open %s: %w", path, err)
	}
	// Only the per-tenant tables live in an org DB; the shared tables (Plan,
	// MeterLease) stay on coord (Postgres) and are never synced here.
	if err := engine.Sync2(perOrgModels()...); err != nil {
		_ = engine.Close()
		return nil, fmt.Errorf("visor: base store sync %s: %w", path, err)
	}

	s.engines[owner] = engine
	s.repl.ship(owner, path) // back this org's DB up to the object store on an interval
	return engine, nil
}

// Shared returns the `_global` SQLite coordination engine that holds the cross-pod
// tables (Plan catalog + the billing leases). Under Base this is a pod-local file, so
// the billing leases are made cluster-safe by the single-writer gate (coordinator.go)
// plus PullSharedLeases/PushShared across a leadership handoff — NOT by the file being
// shared. Under Postgres the same call resolves to the one shared, linearizable engine.
func (s *baseStore) Shared() *xorm.Engine { return s.coord }

// PullSharedLeases merges the object store's committed billing-lease rows into the
// live `_global` coord engine, so a replica that has just become the billing owner
// sees the PRIOR owner's claims BEFORE it claims anything itself. This is guarantee
// (2) of exactly-once (durable lease history across handoff): with it, the insert-once
// PK holds GLOBALLY — a new owner finds a prior claim already present and does not
// re-bill the hour/unit.
//
// It is a ROW MERGE, not a file swap: the remote snapshot is read through a transient
// handle and each lease row is inserted-if-absent into the live coord. That is the
// only pull that is safe while the coord engine is open for process life (a file-level
// RestoreFile requires closing the engine, which would strand the cached Shared()
// pointer). The merge is idempotent — an already-present PK is a no-op — so it is safe
// to run before every claim.
//
// A nil replicator (REPLICA_STORE unset ⇒ local-only, single writer) has nothing to
// pull; it returns nil. A missing remote (no owner has ever shipped) is likewise nil.
func (s *baseStore) PullSharedLeases() error {
	return s.repl.mergeSharedLeases(coordKey, s.coord)
}

// PushShared snapshots the `_global` coord DB and ships it to the object store — the
// post-claim durability step. It is called SYNCHRONOUSLY after a winning lease insert
// and BEFORE the caller debits, so a claim is durable (visible to the next owner)
// before any money moves. SnapshotFile uses a transient read handle (VACUUM INTO), so
// it is safe concurrent with the live coord engine.
//
// A nil replicator (local-only) has nowhere to ship; it returns nil.
func (s *baseStore) PushShared() error {
	return s.repl.pushNow(coordKey, DBPath(s.root, coordKey))
}

// AllEngines returns one engine per org DB under the data root -- the set a
// cross-org sweep unions over. It reads the on-disk orgs/ directory so it sees
// every org ever written, not only those opened this process lifetime.
func (s *baseStore) AllEngines() ([]*xorm.Engine, error) {
	orgsDir := filepath.Join(s.root, "orgs")
	entries, err := os.ReadDir(orgsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no org has been written yet
		}
		return nil, fmt.Errorf("visor: base store list orgs %s: %w", orgsDir, err)
	}

	engines := make([]*xorm.Engine, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// `_global` holds the shared coord tables (Plan/MeterLease), not a tenant —
		// a cross-ORG sweep (billing node-pool report, stale-session GC) must skip it.
		if e.Name() == "_global" {
			continue
		}
		engine, err := s.EngineFor(e.Name())
		if err != nil {
			return nil, err
		}
		engines = append(engines, engine)
	}
	return engines, nil
}

// Close closes every open per-org engine, returning the first error. It does
// NOT close coord: the Adapter owns the shared Postgres engine's lifecycle.
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
