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

// models is visor's single table registry. Schema sync (Adapter.createTable)
// and the Postgres->Base migration both iterate this list, so the set of
// persisted tables is defined in exactly one place.
func models() []interface{} {
	return []interface{}{
		new(Asset),
		new(Provider),
		new(Machine),
		new(Record),
		new(Session),
		new(NodePool),
		new(Plan),
		new(Volume),
		new(AgentBinding),
		new(MeterLease),
	}
}

// engineProvider resolves the *xorm.Engine that serves a given owner (org).
// It is the ONE seam between the shared-Postgres backend (a single engine for
// every owner) and the Base backend (one per-org SQLite engine). Query and
// model code is identical on either side; only the resolved engine differs.
type engineProvider interface {
	// EngineFor returns the engine that serves owner. Postgres ignores owner
	// (isolation is a WHERE owner=? clause); Base returns, lazily opening, the
	// org's own SQLite engine.
	EngineFor(owner string) (*xorm.Engine, error)
	// Close releases every engine the provider owns.
	Close() error
}

// store is the process-wide provider selected at boot by InitStore. New,
// backend-agnostic code routes through object.EngineFor; existing model code
// keeps using adapter.engine (the shared Postgres engine) until call sites are
// migrated in a follow-up slice. Under the default postgres backend both
// resolve to the same engine, so slice-1 is a no-op for the live path.
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
		bs, err := newBaseStore(dataRoot())
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
