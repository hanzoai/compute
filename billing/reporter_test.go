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
	"fmt"
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
// The temp root is set as `dataRoot`, the CONFIG KEY, and that is the whole
// difference between a hermetic suite and one that writes to the machine.
// conf.GetConfigString reads an environment variable named for the key first and
// only then falls back to app.conf — where `dataRoot = ${DATA_ROOT||/data}` is
// expanded ONCE, on the first read of any key. object's own package init reads
// one (logPostOnly), and package init runs before TestMain, so DATA_ROOT set
// here arrived after the answer was already cached: every run of this suite went
// to the REAL /data, accumulated its rows there, and a second run of a test that
// plants a row failed on the primary key.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "visor-billing-store-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("dataRoot", root); err != nil {
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
	metered, skipped := meterPools(context.Background(), nil, []*object.NodePool{pool}, created.Add(20*time.Minute))
	if metered != 0 || skipped != 0 {
		t.Fatalf("the create hour is the create path's: metered=%d skipped=%d, want 0/0", metered, skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("the create hour must not be billed twice, got %d debits", got)
	}

	// Hours 2, 3, 4 — one full pool-hour each, every hour, forever.
	for i, at := range []time.Time{created.Add(time.Hour), created.Add(2 * time.Hour), created.Add(3 * time.Hour)} {
		metered, skipped = meterPools(context.Background(), nil, []*object.NodePool{pool}, at)
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

	if metered, _ := meterPools(context.Background(), nil, []*object.NodePool{activePool(created)}, created.Add(time.Hour)); metered != 1 {
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

	metered, skipped := meterPools(context.Background(), nil, []*object.NodePool{pool}, created.Add(time.Hour))
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

	metered, skipped := meterPools(context.Background(), nil, []*object.NodePool{deleted, empty, orphan}, at)
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
func TestUnitRate_PrefersTheStampedRate(t *testing.T) {
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	cents, err := unitRate(unit{size: "a-slug-the-upstream-delisted", centsHour: 3178})
	if err != nil {
		t.Fatalf("a pool carrying its own rate must price even off-catalog: %v", err)
	}
	if cents != 3178 {
		t.Fatalf("unitRate = %d, want the stamped 3178", cents)
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
	if metered, _ := meterPools(context.Background(), nil, []*object.NodePool{stored}, created); metered != 0 {
		t.Fatalf("the create hour must not be billed twice, metered=%d", metered)
	}
	// Hour two, and every hour after.
	for i := 1; i <= 3; i++ {
		if metered, skipped := meterPools(context.Background(), nil, []*object.NodePool{stored}, created.Add(time.Duration(i)*time.Hour)); metered != 1 {
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

// ---- the provider is the authority for the house account ----

// housePool is a live pool as the provider reports it: 4 nodes of H100 in a
// cluster owned by org.
func housePool(org, clusterID, poolID, name string, nodes int) service.HousePool {
	return service.HousePool{
		Org: org, ClusterID: clusterID, PoolID: poolID, Name: name,
		Size: "gpu-h100x8-640gb", Nodes: nodes,
	}
}

// unitsOf resolves what an hour would bill, keyed by the pool identity in the
// debit. It is the level RED-1, RED-2 and RED-7 are decided at: which pools are
// billable, whose they are, and for how many nodes. Pricing is a separate
// question (unitRate), and one this package's fake deliberately cannot answer —
// commerceOf clears the house token so the resale catalog stays empty.
func unitsOf(live []service.HousePool, rows []*object.NodePool) map[string]unit {
	out := map[string]unit{}
	for _, u := range billableUnits(live, rows) {
		out[u.id] = u
	}
	return out
}

// RED-1. Two clusters, two seed pools, ONE name — which is a thing any tenant can
// do twice in a row, because the seed pool's name comes out of the create body.
//
// The rows collide on (Owner, Name), so recording the second one used to
// overwrite the first: one row survived, pointing at the second cluster, and the
// FIRST cluster stopped being anything the sweep could see. It ran on Hanzo's
// house account for free for the rest of its life. The store here is left in
// exactly that state — one row, naming the second cluster.
//
// Both clusters are live pools, so both are billed, and the surviving row decides
// neither of them.
func TestBothSameNamedClustersAreBilled(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	created := at.Add(-3 * time.Hour).Format(time.RFC3339)

	rows := []*object.NodePool{{
		Owner: "acme", Name: "pool", OrgID: "acme", ProjectID: "research",
		ClusterID: "cl-second", Size: "gpu-h100x8-640gb", Count: 4, State: "Active",
		CostPerHour: 3178, CreatedTime: created,
	}}
	first := housePool("acme", "cl-first", "p-first", "pool", 4)
	first.Created = created
	second := housePool("acme", "cl-second", "p-second", "pool", 4)
	second.Created = created

	units := unitsOf([]service.HousePool{first, second}, rows)
	if len(units) != 2 {
		t.Fatalf("both clusters must be billable, got %d units: %+v", len(units), units)
	}
	for _, id := range []string{"p-first", "p-second"} {
		u, ok := units[id]
		if !ok {
			t.Fatalf("cluster pool %s is not billed at all — it runs free: %+v", id, units)
		}
		if u.org != "acme" || u.nodes != 4 {
			t.Fatalf("%s must bill four nodes to acme, got org=%q nodes=%d", id, u.org, u.nodes)
		}
	}
	// Each pool is keyed on its own upstream id, so two clusters never collapse
	// onto one reconciliation line the way their rows collapsed onto one key.
	if units["p-first"].id == units["p-second"].id {
		t.Fatal("two clusters shared one billing identity")
	}

	// And the money moves for the one the surviving row can price. The other
	// prices from the live resale catalog, which a house account always has and
	// this fake deliberately does not.
	if metered, _ := meterPools(context.Background(), []service.HousePool{second}, rows, at); metered != 1 {
		t.Fatalf("the second cluster must bill, metered=%d", metered)
	}
	if d := f.all(); len(d) != 1 || d[0].amount != 3178*4 || d[0].org != "acme" {
		t.Fatalf("want one four-node debit to acme, got %+v", d)
	}
}

// RED-2. A delete that omits the pool id used to drop the meter-of-record row
// without ever contacting the provider — the cluster kept running and nothing
// billed it.
//
// The row is no longer the meter of record for a house cluster, so even a row
// that is GONE leaves the pool billing: the provider still reports it.
func TestADroppedRowDoesNotStopBillingALiveCluster(t *testing.T) {
	at := time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)

	// The row is gone. The cluster is not.
	live := []service.HousePool{housePool("acme", "cl-1", "p-1", "gpu", 4)}
	live[0].Created = at.Add(-5 * time.Hour).Format(time.RFC3339)

	units := unitsOf(live, nil)
	u, ok := units["p-1"]
	if !ok {
		t.Fatalf("a live pool with no row must still bill, got %+v", units)
	}
	if u.org != "acme" || u.nodes != 4 {
		t.Fatalf("it must bill four nodes to acme, got org=%q nodes=%d", u.org, u.nodes)
	}
	// With no row there is no stamped rate, so the live catalog prices it — and
	// refuses rather than billing zero when it cannot.
	if u.centsHour != 0 {
		t.Fatalf("a row-less pool carries no stamped rate, got %d", u.centsHour)
	}
}

// RED-7. A pool that autoscaled from one node to sixteen. Nothing wrote the new
// count down: the upstream grows the pool on its own and no request reaches
// visor, so the row still says one. Billing the row bills one-sixteenth of what
// is running.
func TestAnAutoscaledPoolBillsTheNodesItGrewTo(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 16, 0, 0, 0, time.UTC)

	rows := []*object.NodePool{{
		Owner: "acme", Name: "gpu", OrgID: "acme", ProjectID: "research",
		ClusterID: "cl-1", PoolID: "p-1", Size: "gpu-h100x8-640gb",
		Count: 1, MinNodes: 1, MaxNodes: 16, AutoScale: true, State: "Active",
		CostPerHour: 3178, CreatedTime: at.Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	live := []service.HousePool{housePool("acme", "cl-1", "p-1", "gpu", 16)}

	if metered, skipped := meterPools(context.Background(), live, rows, at); metered != 1 || skipped != 0 {
		t.Fatalf("metered=%d skipped=%d, want 1/0", metered, skipped)
	}
	debits := f.all()
	if len(debits) != 1 {
		t.Fatalf("want one debit, got %d", len(debits))
	}
	if debits[0].amount != 3178*16 {
		t.Fatalf("an autoscaled pool bills what it GREW to: got %d cents, want %d (the row would say %d)",
			debits[0].amount, 3178*16, 3178)
	}
	// The row still supplies what the provider does not know.
	if debits[0].model != "gpu-h100x8-640gb" {
		t.Fatalf("the debit must name the size, got %q", debits[0].model)
	}
}

// A pool that shrank bills what it shrank TO, by the same rule.
func TestAScaledDownPoolBillsTheNodesItHasLeft(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 17, 0, 0, 0, time.UTC)

	rows := []*object.NodePool{{
		Owner: "acme", Name: "gpu", OrgID: "acme", ClusterID: "cl-1", PoolID: "p-1",
		Size: "gpu-h100x8-640gb", Count: 16, State: "Active", CostPerHour: 3178,
		CreatedTime: at.Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	live := []service.HousePool{housePool("acme", "cl-1", "p-1", "gpu", 2)}

	meterPools(context.Background(), live, rows, at)
	if d := f.all(); len(d) != 1 || d[0].amount != 3178*2 {
		t.Fatalf("a shrunk pool must bill its remaining two nodes, got %+v", d)
	}
}

// A pool the provider no longer reports is not billed, whatever the row says. A
// row outliving its cluster is not stale data — it is an invoice for nodes that
// do not exist.
func TestARowWhoseHousePoolIsGoneStopsBilling(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 18, 0, 0, 0, time.UTC)

	rows := []*object.NodePool{
		{Owner: "acme", Name: "gone", OrgID: "acme", ClusterID: "cl-1", PoolID: "p-gone",
			Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
			CreatedTime: at.Add(-9 * time.Hour).Format(time.RFC3339)},
		{Owner: "acme", Name: "live", OrgID: "acme", ClusterID: "cl-1", PoolID: "p-live",
			Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
			CreatedTime: at.Add(-9 * time.Hour).Format(time.RFC3339)},
	}
	// The cluster is up; only one of its two pools still is.
	live := []service.HousePool{housePool("acme", "cl-1", "p-live", "live", 4)}

	if metered, _ := meterPools(context.Background(), live, rows, at); metered != 1 {
		t.Fatalf("only the surviving pool bills, metered=%d", metered)
	}
	if d := f.all(); len(d) != 1 || d[0].request != "pool-p-live-2026080618" {
		t.Fatalf("the deleted pool was invoiced: %+v", d)
	}
}

// A tenant's OWN cloud account is not the house account, and the house token
// cannot enumerate it. Those rows are the only record there is, so they keep
// billing from the row — and they are never double-billed, because the split is
// on the cluster: a cluster is either one the provider reported or it is not.
func TestBYOCPoolsStillBillFromTheirRow(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 19, 0, 0, 0, time.UTC)

	rows := []*object.NodePool{
		{Owner: "acme", Name: "byoc", OrgID: "acme", ClusterID: "cl-tenant-own", PoolID: "p-byoc",
			Provider: "acme-do", Size: "gpu-h100x8-640gb", Count: 3, State: "Active",
			CostPerHour: 3178, CreatedTime: at.Add(-4 * time.Hour).Format(time.RFC3339)},
		{Owner: "acme", Name: "house", OrgID: "acme", ClusterID: "cl-house", PoolID: "p-house",
			Size: "gpu-h100x8-640gb", Count: 4, State: "Active",
			CostPerHour: 3178, CreatedTime: at.Add(-4 * time.Hour).Format(time.RFC3339)},
	}
	live := []service.HousePool{housePool("acme", "cl-house", "p-house", "house", 4)}

	if metered, skipped := meterPools(context.Background(), live, rows, at); metered != 2 || skipped != 0 {
		t.Fatalf("both the BYOC pool and the house pool bill exactly once: metered=%d skipped=%d", metered, skipped)
	}
	amounts := map[int64]int{}
	for _, d := range f.all() {
		amounts[d.amount]++
	}
	if amounts[3178*3] != 1 || amounts[3178*4] != 1 || len(amounts) != 2 {
		t.Fatalf("want one 3-node BYOC debit and one 4-node house debit, got %v", amounts)
	}
}

// The row is a cache, and a cache that disagrees with the provider about the SIZE
// is a cache holding the wrong price. Pricing from the live catalog is right;
// billing an H100 pool at the rate stamped for the CPU pool that used to carry
// its name is not.
func TestAStaleSizeInTheRowDoesNotPriceTheLivePool(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 20, 0, 0, 0, time.UTC)

	rows := []*object.NodePool{{
		Owner: "acme", Name: "gpu", OrgID: "acme", ClusterID: "cl-1", PoolID: "p-1",
		Size: "s-1vcpu-1gb", Count: 1, State: "Active", CostPerHour: 1,
		CreatedTime: at.Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	live := []service.HousePool{housePool("acme", "cl-1", "p-1", "gpu", 4)} // now H100

	metered, skipped := meterPools(context.Background(), live, rows, at)
	if metered != 0 || skipped != 1 {
		t.Fatalf("the stale one-cent rate must not price an H100 pool: metered=%d skipped=%d", metered, skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("a pool that cannot be priced bills NOTHING, got %d debits", got)
	}
}

// An untagged house cluster is unattributable. It is reported rather than
// silently skipped, so somebody learns a cluster is running for nobody.
func TestAnUntaggedHouseClusterIsALoudSkip(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 21, 0, 0, 0, time.UTC)

	live := []service.HousePool{housePool("", "cl-orphan", "p-1", "gpu", 4)}

	if metered, skipped := meterPools(context.Background(), live, nil, at); metered != 0 || skipped != 1 {
		t.Fatalf("an unattributable pool is a loud skip: metered=%d skipped=%d, want 0/1", metered, skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("want no debits, got %d", got)
	}
}

// The create hour still belongs to the provision path, and the provider's own
// cluster creation time is what says so for a pool with no row.
func TestALivePoolCreatedThisHourIsNotBilledTwice(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 22, 30, 0, 0, time.UTC)

	live := []service.HousePool{housePool("acme", "cl-1", "p-1", "gpu", 4)}
	live[0].Created = at.Add(-20 * time.Minute).Format(time.RFC3339) // same UTC hour

	if metered, skipped := meterPools(context.Background(), live, nil, at); metered != 0 || skipped != 0 {
		t.Fatalf("the create hour is the create path's: metered=%d skipped=%d", metered, skipped)
	}
	if got := len(f.all()); got != 0 {
		t.Fatalf("the create hour must not be billed twice, got %d debits", got)
	}
}

// ---- a provider blip is not a discount ----

// storedPool plants one billable row: created an hour ago, so the sweep owns this
// hour rather than the create path. A row that NAMES a provider runs on that
// tenant's own credentials; a row that names none is on Hanzo's house account.
func storedPool(t *testing.T, org, provider, clusterID string, at time.Time) {
	t.Helper()
	if _, err := object.AddNodePool(&object.NodePool{
		Owner: org, Name: "gpu", OrgID: org, Provider: provider,
		ClusterID: clusterID, PoolID: "pool-" + org,
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
		CreatedTime: at.Add(-time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AddNodePool(%s): %v", org, err)
	}
}

// debitedOrgs is the set of orgs this sweep actually charged. The store is shared
// by the whole package, so what matters is WHICH orgs were billed, never how many
// debits there were in total.
func debitedOrgs(f *commerceFake) map[string]bool {
	out := map[string]bool{}
	for _, d := range f.all() {
		out[d.org] = true
	}
	return out
}

// TestAProviderBlipStillBillsTheTenantsOwnPools is the hour the sweep used to
// throw away.
//
// ListHousePools walks every house cluster one call at a time, so one timeout or
// one 5xx anywhere in that walk failed the whole thing — and the sweep returned,
// billing NOTHING. The hour lease was already claimed by then, so that hour was
// spent: no retry, no reconciliation, and every tenant-provisioned pool ran free
// for it.
//
// Those pools were never the provider's to speak for. They are on the tenant's
// own credentials and invisible to the house token, so the live set could not
// have contained them whether or not the call succeeded. Not billing them is not
// caution — it is a discount for a blip on somebody else's account.
func TestAProviderBlipStillBillsTheTenantsOwnPools(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 23, 0, 0, 0, time.UTC)

	storedPool(t, "blip-tenant", "do", "cl-tenant", at) // the tenant's own credentials
	storedPool(t, "blip-house", "", "cl-house", at)     // Hanzo's house account

	down := func(context.Context) ([]service.HousePool, error) {
		return nil, fmt.Errorf("Get \"https://api.digitalocean.com/v2/kubernetes/clusters\": context deadline exceeded")
	}
	meterNodePools(context.Background(), down, at)

	billed := debitedOrgs(f)
	if !billed["blip-tenant"] {
		t.Fatal("a pool on the tenant's own credentials must still be billed: the house account never ran it, " +
			"so an unreachable house account says nothing about it")
	}
	if billed["blip-house"] {
		t.Fatal("a house pool must NOT be billed from a stale row: with no live set there is no telling " +
			"a running pool from a deleted one")
	}
}

// The control, and without it the test above passes just as well against a sweep
// that has stopped billing house pools altogether: with the provider REACHABLE,
// the house row's cluster is live and it bills.
func TestTheHousePoolBillsWhenTheProviderAnswers(t *testing.T) {
	f := commerceOf(t)
	at := time.Date(2026, 8, 6, 23, 0, 0, 0, time.UTC)

	storedPool(t, "ok-house", "", "cl-ok-house", at)

	up := func(context.Context) ([]service.HousePool, error) {
		return []service.HousePool{housePool("ok-house", "cl-ok-house", "pool-ok-house", "gpu", 4)}, nil
	}
	meterNodePools(context.Background(), up, at)

	if !debitedOrgs(f)["ok-house"] {
		t.Fatal("a live house pool must be billed when the provider answers")
	}
}

// TestARowFromAnotherOrgIsNotThisPoolsRow pins the invariant the pool cache rests
// on, rather than trusting it.
//
// The cache is keyed on (cluster, pool) and (cluster, name) across EVERY org's
// rows — there is no tenant in the index — so the only thing standing between one
// org's row and another org's bill is that writing a row against a house cluster
// requires the house token. Nothing in this package asserts that, and an
// invariant asserted nowhere is one a future write path can break quietly: the
// row contributes the STAMPED RATE and the PROJECT, so a foreign hit prices one
// tenant's pool from another tenant's row.
//
// The pool still bills — from the live catalog, which is the honest answer for a
// pool whose row cannot be found — and it bills its own org.
func TestARowFromAnotherOrgIsNotThisPoolsRow(t *testing.T) {
	live := housePool("rightful", "cl-shared", "pool-shared", "gpu", 4)
	foreign := &object.NodePool{
		Owner: "interloper", Name: "gpu", OrgID: "interloper", ProjectID: "theirs",
		ClusterID: "cl-shared", PoolID: "pool-shared",
		Size: "gpu-h100x8-640gb", Count: 1, State: "Active", CostPerHour: 1,
	}

	u, ok := unitsOf([]service.HousePool{live}, []*object.NodePool{foreign})["pool-shared"]
	if !ok {
		t.Fatal("the live pool must still be billable without a row of its own")
	}
	if u.org != "rightful" {
		t.Fatalf("org = %q, want the cluster tag's — a row cannot redirect a bill", u.org)
	}
	if u.centsHour != 0 {
		t.Fatalf("centsHour = %d, want 0 (the catalog answers) — another org's stamped rate priced this pool", u.centsHour)
	}
	if u.project != "" {
		t.Fatalf("project = %q, want none — another org's attribution reached this pool", u.project)
	}
}

// The control: the pool's OWN row is still found, and it still contributes the
// rate and the project. Without this, the check above passes just as well against
// a cache that finds nothing at all.
func TestAPoolsOwnRowIsStillFound(t *testing.T) {
	live := housePool("rightful", "cl-own", "pool-own", "gpu", 4)
	own := &object.NodePool{
		Owner: "rightful", Name: "gpu", OrgID: "rightful", ProjectID: "research",
		ClusterID: "cl-own", PoolID: "pool-own",
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
	}

	u := unitsOf([]service.HousePool{live}, []*object.NodePool{own})["pool-own"]
	if u.centsHour != 3178 || u.project != "research" {
		t.Fatalf("a pool's own row must still contribute its rate and project: %+v", u)
	}
}

// An UNTAGGED cluster has no org of its own to disagree with, and that is exactly
// the case the row is consulted for the org in the first place. Dropping the row
// there would turn an attributable pool into an unbillable one.
func TestAnUntaggedClusterStillReadsItsRow(t *testing.T) {
	live := housePool("", "cl-untagged", "pool-untagged", "gpu", 4)
	row := &object.NodePool{
		Owner: "acme", Name: "gpu", OrgID: "acme", ProjectID: "research",
		ClusterID: "cl-untagged", PoolID: "pool-untagged",
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
	}

	u := unitsOf([]service.HousePool{live}, []*object.NodePool{row})["pool-untagged"]
	if u.org != "acme" || u.centsHour != 3178 {
		t.Fatalf("an untagged cluster is attributed and priced by its row: %+v", u)
	}
}
