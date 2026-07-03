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
	"path/filepath"

	"github.com/hanzoai/visor/conf"
	"xorm.io/xorm"
)

// perOrgModels are the per-TENANT tables. Under the Base backend each gets one
// physical copy per org (its own SQLite file, routed by Owner); under Postgres
// isolation is a WHERE owner=? clause over one shared table. Every model here
// carries an Owner, so it routes cleanly to a per-org DB.
func perOrgModels() []interface{} {
	return []interface{}{
		new(Asset),
		new(Provider),
		new(Machine),
		new(Record),
		new(Session),
		new(NodePool),
		new(Volume),
		new(AgentBinding),
	}
}

// sharedModels are the cross-pod SHARED tables. They live on the ONE engine
// every pod sees (Postgres under both backends), NEVER in per-org SQLite:
//   - Plan is a global read-only catalog; duplicating it per org would break DRY
//     and let an admin price edit diverge pod-to-pod.
//   - MeterLease is a cluster-global leader-election lease; a single linearizable
//     winner per hour is the whole point, which per-pod SQLite cannot provide.
//
// See LLM.md (Base backend: shared vs per-org) for the full CTO rationale.
func sharedModels() []interface{} {
	return []interface{}{
		new(Plan),
		new(MeterLease),
	}
}

// models is visor's full table registry -- the union of per-org and shared
// tables, defined in exactly one place. The Postgres adapter (Adapter.createTable)
// hosts every table and syncs this full set; the Base backend syncs only
// perOrgModels into each org DB and leaves sharedModels on Postgres.
func models() []interface{} {
	return append(perOrgModels(), sharedModels()...)
}

// engineProvider resolves the *xorm.Engine(s) that serve visor's tables. It is
// the ONE seam between the shared-Postgres backend (a single engine for every
// owner) and the Base backend (one per-org SQLite engine plus a shared Postgres
// coordination engine). Query and model code is identical on either side; only
// the resolved engine differs.
type engineProvider interface {
	// EngineFor returns the engine that serves owner's per-tenant rows.
	// Postgres ignores owner (isolation is a WHERE owner=? clause); Base
	// returns, lazily opening, the org's own SQLite engine.
	EngineFor(owner string) (*xorm.Engine, error)
	// Shared returns the ONE engine that holds the cross-pod shared tables
	// (the Plan catalog and the MeterLease lease). Postgres under both backends.
	Shared() *xorm.Engine
	// AllEngines returns every engine a cross-org sweep must union over.
	// Postgres: the single engine (it already holds every org's rows).
	// Base: one engine per org DB under the data root.
	AllEngines() ([]*xorm.Engine, error)
	// Close releases every engine the provider owns.
	Close() error
}

// store is the process-wide provider selected at boot by InitStore. All model
// code routes per-tenant queries through EngineFor(owner), shared-table queries
// through Shared(), and cross-org sweeps through allEngines(). Under the default
// postgres backend all three resolve to the same one engine, so the live path is
// byte-identical to before the seam existed.
var store engineProvider

// StorageBackend names the configured persistence backend.
type StorageBackend string

const (
	// BackendPostgres is the historical shared-Postgres backend (default).
	BackendPostgres StorageBackend = "postgres"
	// BackendBase is the per-org SQLite substrate (hanzoai/base, HIP-0302).
	BackendBase StorageBackend = "base"
)

// ConfiguredBackend reads the storageBackend config knob (default "postgres").
func ConfiguredBackend() StorageBackend {
	if conf.GetConfigString("storageBackend") == string(BackendBase) {
		return BackendBase
	}
	return BackendPostgres
}

// InitStore selects and installs the process-wide engine provider. It is called
// by InitAdapter after the Postgres adapter is built. The Postgres engine is
// ALWAYS opened (the live query path depends on it, and the migration reads
// from it); the flag only decides what EngineFor hands back to new code.
func InitStore() {
	switch ConfiguredBackend() {
	case BackendBase:
		// adapter.engine (Postgres) is the shared coordination engine: even in
		// Base mode the Plan catalog and MeterLease lease stay on it.
		bs, err := newBaseStore(dataRoot(), adapter.engine)
		if err != nil {
			panic(fmt.Errorf("visor: init base store: %w", err))
		}
		store = bs
	default:
		store = &pgStore{engine: adapter.engine}
	}
}

// EngineFor returns the storage engine that serves owner under the configured
// backend. This is the single entry point backend-agnostic code MUST use so a
// query works unchanged on either backend.
func EngineFor(owner string) (*xorm.Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("visor: store not initialised (call InitAdapter first)")
	}
	return store.EngineFor(owner)
}

// mustEngineFor resolves owner's engine or panics. Used only where the caller
// has no error return to thread. Under the default Postgres backend EngineFor
// hands back the one already-open engine and cannot fail, so this panics only
// under the Base backend on a broken data disk -- a fatal infra condition,
// surfaced the same way Adapter.createTable's sync failure already is.
func mustEngineFor(owner string) *xorm.Engine {
	engine, err := EngineFor(owner)
	if err != nil {
		panic(err)
	}
	return engine
}

// Shared returns the engine holding the cross-pod shared tables (the Plan
// catalog and the MeterLease coordination lease). Postgres under both backends.
func Shared() *xorm.Engine {
	return store.Shared()
}

// allEngines returns every engine a cross-org sweep (billing node-pool report,
// stale-session GC) must union over. One engine under Postgres; one per org DB
// under Base.
func allEngines() ([]*xorm.Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("visor: store not initialised (call InitAdapter first)")
	}
	return store.AllEngines()
}

// dataRoot is the filesystem root for per-org Base SQLite files (default /data).
func dataRoot() string {
	if r := conf.GetConfigString("dataRoot"); r != "" {
		return r
	}
	return "/data"
}

// DBPath is the HIP-0302 per-org SQLite path for a visor org DB. It mirrors the
// layout hanzo/cloud uses (orgs/<org>/<service>.db) so every Hanzo service that
// moves onto Base shares one on-disk convention.
//
//	DBPath("/data", "acme")  ->  /data/orgs/acme/visor.db
//
// The empty owner (catalog / cluster-global rows that no org owns) routes to the
// _global sentinel, matching hanzo/cloud's fail-loud migration bucket.
func DBPath(root, owner string) string {
	if owner == "" {
		owner = "_global"
	}
	return filepath.Join(root, "orgs", owner, "visor.db")
}
