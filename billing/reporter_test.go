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

package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
)

// TestMain gives this package a REAL per-org store. The property that matters
// here — a cluster's seed pool is billed every hour after its first — spans the
// write (object.RecordSeedPool) and the read (object.GetAllNodePools, the exact
// query the sweep runs) and is a property of neither alone. Testing the halves
// separately is how a create that writes no row at all went unnoticed.
//
// DATA_ROOT is set before anything reads conf, so ${DATA_ROOT||/data} expands to
// a temp dir and the Base backend opens its per-org SQLite there.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "visor-billing-store-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("DATA_ROOT", root); err != nil {
		panic(err)
	}
	object.InitAdapter()
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// ---- fake commerce, recording the WIRE ----

// debit is one recorded POST to commerce: the amount, and the three things the
// node-pool meter used to get wrong — the path it posted to, the credential it
// presented, and the tenant it named.
type debit struct {
	path     string
	auth     string
	org      string
	amount   int64
	provider string
	model    string
	status   string
	request  string
}

type commerceFake struct {
	mu     sync.Mutex
	debits []debit
}

// commerceOf stands up a funded fake commerce and points the metering client at
// it. Recording the raw path/header/body is deliberate: "the sweep debited" is
// not the property under test, "the sweep debited the endpoint commerce actually
// serves, with the credential production actually sets, for the right tenant" is.
func commerceOf(t *testing.T) *commerceFake {
	t.Helper()
	f := &commerceFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"available": 100000000, "currency": "usd"})
			return
		}
		var u struct {
			Amount    int64  `json:"amount"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			Status    string `json:"status"`
			RequestID string `json:"requestId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&u)
		f.mu.Lock()
		f.debits = append(f.debits, debit{
			path: r.URL.Path, auth: r.Header.Get("Authorization"), org: r.Header.Get("X-Org-Id"),
			amount: u.Amount, provider: u.Provider, model: u.Model, status: u.Status, request: u.RequestID,
		})
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"transactionId":"tx","type":"usage"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMMERCE_URL", srv.URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	// No house DigitalOcean token: the resale catalog cannot refresh, so a pool
	// with no stored rate has no price at all — which is exactly the condition the
	// refuse-rather-than-bill-zero case needs.
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	return f
}

func (f *commerceFake) all() []debit {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]debit, len(f.debits))
	copy(out, f.debits)
	return out
}

// activePool is a running GPU pool: 4 nodes at $31.78/node-hour, created at
// created.
func activePool(created time.Time) *object.NodePool {
	return &object.NodePool{
		Owner: "acme", Name: "gpu", OrgID: "acme", ProjectID: "research",
		ClusterID: "cl-1", PoolID: "pool-1", Provider: "do",
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active",
		CostPerHour: 3178,
		CreatedTime: created.UTC().Format(time.RFC3339),
	}
}

// ---- the headline: hours two and after are billed ----

// A pool is billed EVERY hour it runs, not just the hour it was created. The
// create path owns the create hour (it debited it up front) and the sweep owns
// every hour after — so hour 1 is billed exactly once, and hours 2..N are billed
// at all, which they were not.
func TestMeterPools_BillsEveryHourAfterTheFirst(t *testing.T) {
	f := commerceOf(t)
	created := time.Date(2026, 8, 6, 10, 5, 0, 0, time.UTC)
	pool := activePool(created)

	// Hour 1 — the create hour. The create path already debited it.
	metered, skipped := meterPools(context.Background(), []*object.NodePool{pool}, created.Add(20*time.Minute))
	if metered != 0 || skipped != 0 {
		t.Fatalf("the create hour is the create path's: metered=%d skipped=%d, want 0/0", metered, skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("the create hour must not be billed twice, got %d debits", got)
	}

	// Hours 2, 3, 4 — one full pool-hour each, every hour, forever.
	for i, at := range []time.Time{created.Add(time.Hour), created.Add(2 * time.Hour), created.Add(3 * time.Hour)} {
		metered, skipped = meterPools(context.Background(), []*object.NodePool{pool}, at)
		if metered != 1 || skipped != 0 {
			t.Fatalf("hour %d: metered=%d skipped=%d, want 1/0", i+2, metered, skipped)
		}
	}

	debits := f.all()
	if len(debits) != 3 {
		t.Fatalf("three later hours must be three debits, got %d", len(debits))
	}
	seen := map[string]bool{}
	for _, d := range debits {
		if d.amount != 3178*4 {
			t.Fatalf("a pool-hour is rate x nodes: got %d cents, want %d", d.amount, 3178*4)
		}
		if d.provider != "compute" || d.model != "gpu-h100x8-640gb" || d.status != "running" {
			t.Fatalf("debit must name the compute plane, the size and the lifecycle point: %+v", d)
		}
		if seen[d.request] {
			t.Fatalf("each hour needs its own idempotency key, saw %q twice", d.request)
		}
		seen[d.request] = true
	}
}

// The three independent reasons this sweep could never have billed anything: it
// posted to a path commerce does not serve, it presented a credential nothing
// sets, and it named no tenant. Asserted on the wire, because every one of them
// is invisible from inside the process.
func TestMeterPools_DebitRidesTheOneCommercePath(t *testing.T) {
	f := commerceOf(t)
	created := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	if metered, _ := meterPools(context.Background(), []*object.NodePool{activePool(created)}, created.Add(time.Hour)); metered != 1 {
		t.Fatalf("a funded, priced, running pool must be billed, metered=%d", metered)
	}
	debits := f.all()
	if len(debits) != 1 {
		t.Fatalf("want exactly one debit, got %d", len(debits))
	}
	d := debits[0]
	if d.path != "/v1/billing/usage" {
		t.Fatalf("commerce serves /v1/billing/usage (no /api/ prefix), the sweep posted to %q", d.path)
	}
	if d.auth != "Bearer svc-token" {
		t.Fatalf("the sweep must present COMMERCE_SERVICE_TOKEN, got %q", d.auth)
	}
	if d.org != "acme" {
		t.Fatalf("the debit must carry its tenant as X-Org-Id, got %q", d.org)
	}
}

// A pool whose rate cannot be resolved is a LOUD skip, never a zero debit. This
// is the mutation target: fall back to 0 instead of refusing and a GPU pool bills
// nothing while it runs, which is the leak wearing a different hat.
func TestMeterPools_RefusesRatherThanBillZero(t *testing.T) {
	f := commerceOf(t)
	created := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	pool := activePool(created)
	pool.CostPerHour = 0 // no stored rate, and the catalog cannot resolve one either

	metered, skipped := meterPools(context.Background(), []*object.NodePool{pool}, created.Add(time.Hour))
	if metered != 0 || skipped != 1 {
		t.Fatalf("an unpriceable pool must be skipped loudly: metered=%d skipped=%d, want 0/1", metered, skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("an unpriceable pool must bill NOTHING, got %d debits", got)
	}
}

// A pool with no stored rate and no billing org is unattributable, and a pool
// that is not running or has no nodes costs nothing. None of them bill.
func TestMeterPools_SkipsWhatCannotOrShouldNotBill(t *testing.T) {
	f := commerceOf(t)
	created := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	at := created.Add(time.Hour)

	deleted := activePool(created)
	deleted.State = "Deleted"
	empty := activePool(created)
	empty.Count = 0
	orphan := activePool(created)
	orphan.Owner, orphan.OrgID = "", ""

	metered, skipped := meterPools(context.Background(), []*object.NodePool{deleted, empty, orphan}, at)
	if metered != 0 {
		t.Fatalf("nothing here is billable, metered=%d", metered)
	}
	if skipped != 1 {
		t.Fatalf("only the unattributable pool is a loud skip, got skipped=%d", skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("want no debits, got %d", got)
	}
}

// The pool's OWN persisted rate wins over the live catalog: it was stamped from
// the catalog at provision time and is the only price a slug the upstream has
// since delisted still has.
func TestPoolHourlyCents_PrefersTheStoredRate(t *testing.T) {
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	pool := activePool(time.Now())
	pool.Size = "a-slug-the-upstream-delisted"

	cents, err := poolHourlyCents(pool)
	if err != nil {
		t.Fatalf("a pool carrying its own rate must price even off-catalog: %v", err)
	}
	if cents != 3178 {
		t.Fatalf("poolHourlyCents = %d, want the stored 3178", cents)
	}
}

// ---- end to end: a provisioned cluster keeps billing ----

// The whole leak, closed, through the real store: a metered cluster create
// records its seed pool, the sweep reads it back through the same query the
// ticker runs, and bills it EVERY hour after the one the create already paid for.
//
// Before this, a /v1/k8s/clusters cluster wrote no row and no org-tagged droplet,
// so both meters were blind to it: it was gated and debited exactly once, at
// create, and then ran on Hanzo's house DigitalOcean account for free.
func TestSeedPoolIsBilledEveryHourAfterTheCreateHour(t *testing.T) {
	f := commerceOf(t)

	seed := service.SeedPool{
		Org: "acme-e2e", Project: "research", ClusterID: "cl-e2e",
		Name: "acme-gpu-pool", Size: "gpu-h100x8-640gb", Count: 4, CentsHour: 3178,
	}
	if err := object.RecordSeedPool(seed); err != nil {
		t.Fatalf("RecordSeedPool: %v", err)
	}

	// The sweep's own query — no hand-built row stands in for the stored one.
	pools := []*object.NodePool{}
	if err := object.GetAllNodePools(&pools); err != nil {
		t.Fatalf("GetAllNodePools: %v", err)
	}
	var stored *object.NodePool
	for _, p := range pools {
		if p.Owner == "acme-e2e" && p.Name == "acme-gpu-pool" {
			stored = p
		}
	}
	if stored == nil {
		t.Fatal("the cluster's seed pool must be a row the cross-org sweep can see")
	}
	if stored.State != "Active" || stored.Count != 4 || stored.CostPerHour != 3178 ||
		stored.OrgID != "acme-e2e" || stored.ProjectID != "research" || stored.ClusterID != "cl-e2e" ||
		stored.Size != "gpu-h100x8-640gb" {
		t.Fatalf("stored pool does not describe what was provisioned: %+v", stored)
	}

	created, err := time.Parse(time.RFC3339, stored.CreatedTime)
	if err != nil {
		t.Fatalf("a billable row needs a parseable create time, got %q: %v", stored.CreatedTime, err)
	}

	// Hour one belongs to the create path, which already debited it.
	if metered, _ := meterPools(context.Background(), []*object.NodePool{stored}, created); metered != 0 {
		t.Fatalf("the create hour must not be billed twice, metered=%d", metered)
	}
	// Hour two, and every hour after.
	for i := 1; i <= 3; i++ {
		if metered, skipped := meterPools(context.Background(), []*object.NodePool{stored}, created.Add(time.Duration(i)*time.Hour)); metered != 1 {
			t.Fatalf("hour %d must be billed: metered=%d skipped=%d", i+1, metered, skipped)
		}
	}
	debits := f.all()
	if len(debits) != 3 {
		t.Fatalf("three hours of running must be three debits, got %d", len(debits))
	}
	for _, d := range debits {
		if d.amount != 3178*4 || d.org != "acme-e2e" || d.path != "/v1/billing/usage" {
			t.Fatalf("debit does not bill the stored pool to its org on the one meter: %+v", d)
		}
	}

	// And the teardown stops it: a row outliving its cluster invoices nodes that
	// no longer exist.
	if err := object.ForgetClusterPools("acme-e2e", "cl-e2e"); err != nil {
		t.Fatalf("ForgetClusterPools: %v", err)
	}
	pools = []*object.NodePool{}
	if err := object.GetAllNodePools(&pools); err != nil {
		t.Fatalf("GetAllNodePools after teardown: %v", err)
	}
	for _, p := range pools {
		if p.Owner == "acme-e2e" && p.ClusterID == "cl-e2e" {
			t.Fatalf("a deleted cluster's pool must stop metering, still present: %+v", p)
		}
	}
}

// Re-recording the same seed pool updates it in place and keeps its ORIGINAL
// create hour, so a retried create can never re-open the exactly-once window on
// an hour the first attempt already debited.
func TestRecordSeedPoolIsIdempotent(t *testing.T) {
	seed := service.SeedPool{Org: "acme-retry", ClusterID: "cl-retry", Name: "p",
		Size: "gpu-h100x8-640gb", Count: 2, CentsHour: 3178}
	if err := object.RecordSeedPool(seed); err != nil {
		t.Fatalf("first record: %v", err)
	}
	first, err := object.GetNodePool("acme-retry/p")
	if err != nil || first == nil {
		t.Fatalf("read back: %v", err)
	}
	if err := object.RecordSeedPool(seed); err != nil {
		t.Fatalf("second record must not fail the primary key: %v", err)
	}
	again, err := object.GetNodePool("acme-retry/p")
	if err != nil || again == nil {
		t.Fatalf("read back: %v", err)
	}
	if again.CreatedTime != first.CreatedTime {
		t.Fatalf("a retried create must keep the original create hour: %q then %q", first.CreatedTime, again.CreatedTime)
	}
}

// No service token means no sweep can debit, and the whole sweep says so rather
// than quietly doing nothing.
func TestMeterRunningNodePools_CannotDebitWithoutAToken(t *testing.T) {
	f := commerceOf(t)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")

	MeterRunningNodePools(context.Background(), time.Now())

	if got := len(f.all()); got != 0 {
		t.Fatalf("an unconfigured meter must debit nothing, got %d", got)
	}
}
