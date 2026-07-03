// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
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
	"testing"
	"time"

	"xorm.io/xorm"
)

// installBaseStore installs a Base-backend engineProvider backed by a temp
// dataRoot plus a temp SQLite engine standing in for the shared Postgres
// coordination engine (Plan catalog, MeterLease). It restores the previous
// process-wide store on cleanup, so it composes with the rest of the package's
// tests. The modernc "sqlite" driver is registered by store_base.go's blank
// import, which this same package shares.
func installBaseStore(t *testing.T) *baseStore {
	t.Helper()
	root := t.TempDir()

	coord, err := xorm.NewEngine("sqlite", root+"/shared.db"+sqlitePragmas)
	if err != nil {
		t.Fatalf("open shared engine: %v", err)
	}
	if err := coord.Sync2(sharedModels()...); err != nil {
		t.Fatalf("sync shared models: %v", err)
	}

	bs, err := newBaseStore(root, coord)
	if err != nil {
		t.Fatalf("newBaseStore: %v", err)
	}

	prev := store
	store = bs
	t.Cleanup(func() {
		_ = bs.Close()
		_ = coord.Close()
		store = prev
	})
	return bs
}

// TestBaseBackendPerOrgIsolation is the core acceptance test: under the Base
// backend, launching work for two orgs creates two distinct per-org SQLite
// files and neither org can see the other's rows.
func TestBaseBackendPerOrgIsolation(t *testing.T) {
	bs := installBaseStore(t)

	if _, err := AddAsset(&Asset{Owner: "org-a", Name: "web", DisplayName: "A web"}); err != nil {
		t.Fatalf("AddAsset org-a: %v", err)
	}
	if _, err := AddMachine(&Machine{Owner: "org-b", Name: "db", DisplayName: "B db"}); err != nil {
		t.Fatalf("AddMachine org-b: %v", err)
	}

	// Each org got its own on-disk visor.db, and org-a's file was created by the
	// asset write (not org-b's).
	for _, org := range []string{"org-a", "org-b"} {
		if _, err := os.Stat(DBPath(bs.root, org)); err != nil {
			t.Fatalf("expected per-org DB for %s at %s: %v", org, DBPath(bs.root, org), err)
		}
	}

	// org-a sees exactly its own asset.
	a, err := GetAssets("org-a")
	if err != nil {
		t.Fatalf("GetAssets org-a: %v", err)
	}
	if len(a) != 1 || a[0].Name != "web" {
		t.Fatalf("GetAssets org-a = %+v, want 1 asset named web", a)
	}

	// org-b has no assets at all (its machine lives in a separate file, and the
	// asset write never touched org-b's DB).
	b, err := GetAssets("org-b")
	if err != nil {
		t.Fatalf("GetAssets org-b: %v", err)
	}
	if len(b) != 0 {
		t.Fatalf("GetAssets org-b = %+v, want none (isolation breach)", b)
	}

	// A cross-org lookup by name must not resolve into the other org's DB.
	if got, err := getMachine("org-a", "db"); err != nil {
		t.Fatalf("getMachine org-a/db: %v", err)
	} else if got != nil {
		t.Fatalf("getMachine org-a/db = %+v, want nil (org-b's machine leaked)", got)
	}
	if got, err := getMachine("org-b", "db"); err != nil {
		t.Fatalf("getMachine org-b/db: %v", err)
	} else if got == nil || got.DisplayName != "B db" {
		t.Fatalf("getMachine org-b/db = %+v, want org-b's machine", got)
	}
}

// TestBaseBackendCrossOrgFanOut proves the cluster-wide sweeps (billing node
// pools, stale-session GC) union every per-org DB rather than reading one table.
func TestBaseBackendCrossOrgFanOut(t *testing.T) {
	installBaseStore(t)

	if _, err := AddNodePool(&NodePool{Owner: "org-a", Name: "pool-a", State: "Active", Count: 1}); err != nil {
		t.Fatalf("AddNodePool org-a: %v", err)
	}
	if _, err := AddNodePool(&NodePool{Owner: "org-b", Name: "pool-b", State: "Active", Count: 2}); err != nil {
		t.Fatalf("AddNodePool org-b: %v", err)
	}
	if _, err := AddSession(&Session{Owner: "org-a", Name: "s-a", Status: NoConnect}); err != nil {
		t.Fatalf("AddSession org-a: %v", err)
	}
	if _, err := AddSession(&Session{Owner: "org-b", Name: "s-b", Status: Connecting}); err != nil {
		t.Fatalf("AddSession org-b: %v", err)
	}

	pools := []*NodePool{}
	if err := GetAllNodePools(&pools); err != nil {
		t.Fatalf("GetAllNodePools: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("GetAllNodePools returned %d pools, want 2 (fan-out across orgs)", len(pools))
	}

	sessions, err := GetSessionsByStatus([]string{NoConnect, Connecting})
	if err != nil {
		t.Fatalf("GetSessionsByStatus: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("GetSessionsByStatus returned %d, want 2 (fan-out across orgs)", len(sessions))
	}
}

// TestBaseBackendSharedTables proves the global catalog and the coordination
// lease live on the shared engine, NOT in any per-org SQLite file.
func TestBaseBackendSharedTables(t *testing.T) {
	bs := installBaseStore(t)

	if _, err := AddPlan(&Plan{Owner: "hanzo", Name: "starter", State: "Active"}); err != nil {
		t.Fatalf("AddPlan: %v", err)
	}
	plans, err := GetPlans("hanzo")
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(plans) != 1 || plans[0].Name != "starter" {
		t.Fatalf("GetPlans = %+v, want 1 plan named starter from the shared catalog", plans)
	}
	// The catalog write must NOT have created a per-org DB for "hanzo".
	if _, err := os.Stat(DBPath(bs.root, "hanzo")); !os.IsNotExist(err) {
		t.Fatalf("catalog write leaked a per-org DB for hanzo (err=%v); Plan must stay on the shared engine", err)
	}

	// MeterLease is a single-winner lease on the shared engine: the first claim
	// for an hour wins, a second claim for the same hour loses.
	now := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	if !ClaimMeterHour(now) {
		t.Fatal("first ClaimMeterHour must win")
	}
	if ClaimMeterHour(now) {
		t.Fatal("second ClaimMeterHour for the same hour must lose (double-debit guard)")
	}
	// A restart within the same hour (re-entry) still loses.
	if ClaimMeterHour(now.Add(30 * time.Minute)) {
		t.Fatal("re-claim within the same wall-clock hour must lose")
	}
	// The next hour is claimable again.
	if !ClaimMeterHour(now.Add(time.Hour)) {
		t.Fatal("next hour must be claimable")
	}
}

// TestBaseStoreAllEnginesEnumeratesOnDisk proves AllEngines discovers every org
// DB from the filesystem, including orgs not yet opened this process lifetime.
func TestBaseStoreAllEnginesEnumeratesOnDisk(t *testing.T) {
	bs := installBaseStore(t)

	// No org written yet: an empty (or absent) orgs dir yields no engines.
	if engines, err := bs.AllEngines(); err != nil {
		t.Fatalf("AllEngines (empty): %v", err)
	} else if len(engines) != 0 {
		t.Fatalf("AllEngines (empty) = %d engines, want 0", len(engines))
	}

	for _, org := range []string{"org-a", "org-b", "org-c"} {
		if _, err := EngineFor(org); err != nil {
			t.Fatalf("EngineFor %s: %v", org, err)
		}
	}
	engines, err := bs.AllEngines()
	if err != nil {
		t.Fatalf("AllEngines: %v", err)
	}
	if len(engines) != 3 {
		t.Fatalf("AllEngines = %d engines, want 3", len(engines))
	}
}

// TestModelRegistrySplit locks in the invariant that the shared tables are never
// synced into a per-org DB, and that models() is exactly the two sets unioned.
func TestModelRegistrySplit(t *testing.T) {
	for _, m := range perOrgModels() {
		switch m.(type) {
		case *Plan, *MeterLease:
			t.Fatalf("perOrgModels must not include shared table %T", m)
		}
	}
	if got, want := len(models()), len(perOrgModels())+len(sharedModels()); got != want {
		t.Fatalf("len(models()) = %d, want perOrg+shared = %d", got, want)
	}
}

func TestDBPath(t *testing.T) {
	if got, want := DBPath("/data", "acme"), "/data/orgs/acme/visor.db"; got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	if got, want := DBPath("/data", ""), "/data/orgs/_global/visor.db"; got != want {
		t.Fatalf("DBPath empty owner = %q, want %q", got, want)
	}
}
