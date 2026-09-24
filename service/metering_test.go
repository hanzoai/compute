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

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/compute/service/commercetest"
)

func TestOrgFromTag(t *testing.T) {
	cases := []struct {
		tags string
		want string
	}{
		{"hanzo-org:acme,", "acme"},
		{"foo,hanzo-org:maxpower,bar", "maxpower"},
		{" hanzo-org:acme ", "acme"},
		{"foo,bar", ""},         // no org tag -> unattributable
		{"", ""},                // empty
		{"hanzo-orgx:acme", ""}, // must be the exact key, not a prefix collision
	}
	for _, c := range cases {
		if got := orgFromTag(c.tags); got != c.want {
			t.Fatalf("orgFromTag(%q) = %q, want %q", c.tags, got, c.want)
		}
	}
}

func TestProjectFromTag(t *testing.T) {
	cases := []struct {
		tags string
		want string
	}{
		{"hanzo-org:acme,hanzo-project:web,", "web"},
		{"foo,hanzo-project:api,bar", "api"},
		{" hanzo-project:web ", "web"},
		{"hanzo-org:acme,", ""},    // org only -> default project
		{"", ""},                   // empty -> default project
		{"hanzo-projectx:web", ""}, // exact key, not a prefix collision
	}
	for _, c := range cases {
		if got := projectFromTag(c.tags); got != c.want {
			t.Fatalf("projectFromTag(%q) = %q, want %q", c.tags, got, c.want)
		}
	}
}

func TestMeterActor(t *testing.T) {
	// Empty project is the default project: the actor stays the bare org, so
	// threading project through the existing debits is a no-op for callers that do
	// not set X-Project-Id (backward-compatible). A named project yields org/project.
	if got := MeterActor("acme", ""); got != "acme" {
		t.Fatalf("MeterActor(acme, \"\") = %q, want acme (default project == today's behavior)", got)
	}
	if got := MeterActor("acme", "web"); got != "acme/web" {
		t.Fatalf("MeterActor(acme, web) = %q, want acme/web", got)
	}
}

func TestNormalizeProject(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"web", "web"},
		{"  web  ", "web"},
		{"bad,project", ""}, // separators can't survive tag/meter read-back -> default
		{"bad:project", ""},
		{"has space", ""},
	}
	for _, c := range cases {
		if got := NormalizeProject(c.in); got != c.want {
			t.Fatalf("NormalizeProject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A running machine carrying a project tag attributes its debit to the project,
// while the debit destination (the org named in the body and X-Org-Id) stays the
// org — one org balance covers all its projects.
func TestMeterMachines_AttributesProjectViaActor(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))

	now := time.Date(2026, 7, 2, 15, 30, 0, 0, time.UTC)
	machines := []*Machine{{Id: "p1", Size: "s", Tag: "hanzo-org:acme,hanzo-project:web,"}}

	metered, _ := meterMachines(context.Background(), machines, now)
	if metered != 1 {
		t.Fatalf("metered=%d, want 1", metered)
	}
	mu.Lock()
	defer mu.Unlock()
	r := (*recs)[0]
	if r.org != "acme" { // debit destination stays the org
		t.Fatalf("X-Org-Id = %q, want acme", r.org)
	}
	if r.usage.Org != "acme" {
		t.Fatalf("debit org = %q, want acme (org is the billing key)", r.usage.Org)
	}
	if r.usage.Project != "web" {
		t.Fatalf("project = %q, want web (per-project attribution)", r.usage.Project)
	}
}

func TestHourStamp_IdempotencyBucket(t *testing.T) {
	base := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	// Same hour -> same stamp (debit dedups).
	if a, b := HourStamp(base), HourStamp(base.Add(59*time.Minute)); a != b {
		t.Fatalf("same-hour stamps differ: %q vs %q", a, b)
	}
	// Next hour -> different stamp (a new hour is a new billable unit).
	if a, b := HourStamp(base), HourStamp(base.Add(time.Hour)); a == b {
		t.Fatalf("cross-hour stamps equal: %q", a)
	}
	if got := HourStamp(base); got != "2026070215" {
		t.Fatalf("hourStamp = %q, want 2026070215", got)
	}
}

// recorded is one usage debit captured by the fake commerce.
type recorded struct {
	org   string
	auth  string
	usage commercetest.Usage
}

// fakeCommerce records every usage POST; it never gates (a debit does not check
// balance).
func fakeCommerce(t *testing.T) (got *[]recorded, mu *sync.Mutex) {
	t.Helper()
	freshLedger(t)
	var recs []recorded
	var m sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		u := commercetest.Read(r)
		m.Lock()
		recs = append(recs, recorded{org: r.Header.Get("X-Org-Id"), auth: r.Header.Get("Authorization"), usage: u})
		m.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+u.ID+`","org":"`+u.Org+`","account":"`+u.Org+`","amount":{"decimal":"`+u.Amount.Decimal+`","currency":"usd"}}`)
	})
	commercetest.Serve(t, mux)
	return &recs, &m
}

// freshLedger gives one test a ledger of its own, so no hour another test billed
// reads as billed here.
func freshLedger(t *testing.T) {
	t.Helper()
	saved := billed()
	RegisterLedger(newMemoryLedger())
	t.Cleanup(func() { RegisterLedger(saved) })
}

// seedCatalog replaces the catalog for one test, so a size prices at exactly
// what the test says.
func seedCatalog(t *testing.T, sale ...offer) {
	t.Helper()
	saved := offers
	offers = sale
	t.Cleanup(func() { offers = saved })
}

// priced is an offer whose Hanzo price is exactly cents per running hour: its
// list price is the cost that, with the public address and the fee, is that many
// cents, with no disk.
func priced(slug string, cents int64) offer {
	return offer{slug: slug, listMicros: cents*microsPerCent*feeDen/feeNum - ipv4MicrosPerHour}
}

// A running machine debits its OWNING org one hour of its price, attributed
// product "compute" / model <size>, with an hour-bucketed idempotency RequestID.
func TestMeterMachines_DebitsRunningMachinePerOrg(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("m7i.large", 5))

	now := time.Date(2026, 7, 2, 15, 30, 0, 0, time.UTC)
	machines := []*Machine{{Id: "111", Size: "m7i.large", Tag: "hanzo-org:acme,"}}

	metered, skipped := meterMachines(context.Background(), machines, now)
	if metered != 1 || skipped != 0 {
		t.Fatalf("metered=%d skipped=%d, want 1/0", metered, skipped)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 1 {
		t.Fatalf("recorded %d debits, want 1", len(*recs))
	}
	r := (*recs)[0]
	if r.org != "acme" {
		t.Fatalf("X-Org-Id = %q, want acme", r.org)
	}
	u := r.usage
	if u.Org != "acme" {
		t.Fatalf("debit org = %q, want acme", u.Org)
	}
	if r.auth != "Bearer "+commercetest.Token {
		t.Fatalf("debit presented %q, want visor's IAM token", r.auth)
	}
	if u.Service != "compute" {
		t.Fatalf("service = %q, want compute", u.Service)
	}
	if u.Model != "m7i.large" {
		t.Fatalf("model = %q, want m7i.large", u.Model)
	}
	if u.Amount.Decimal != "0.05" || u.Amount.Currency != "usd" || u.Cents() != 5 { // $0.05 -> 5 cents
		t.Fatalf("amount = %+v, want 0.05 usd", u.Amount)
	}
	if u.ID != "compute-111-2026070215" {
		t.Fatalf("id = %q, want compute-111-2026070215 (hour-bucketed idempotency)", u.ID)
	}
}

// A second sweep in the SAME hour bills nothing — the ledger already holds the
// hour — and the NEXT hour is a new billable unit under a new id.
func TestMeterMachines_IdempotentWithinHour(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("t3.medium", 1))

	m := []*Machine{{Id: "222", Size: "t3.medium", Tag: "hanzo-org:acme"}}
	h15 := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	meterMachines(context.Background(), m, h15)
	meterMachines(context.Background(), m, h15.Add(50*time.Minute)) // same hour
	meterMachines(context.Background(), m, h15.Add(time.Hour))      // next hour

	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 2 {
		t.Fatalf("recorded %d, want 2 — a second sweep in one hour charged it again", len(*recs))
	}
	if id1, id2 := (*recs)[0].usage.ID, (*recs)[1].usage.ID; id1 != "compute-222-2026070215" || id2 != "compute-222-2026070216" {
		t.Fatalf("ids = %q, %q", id1, id2)
	}
}

// Hours a sweep missed are billed by the next one, each once. A machine billed
// through 16:00 whose next sweep is at 20:10 — the ticker restarted, the
// provider was down, a tick failed — owes 17, 18, 19 and 20, and a second sweep
// in the same hour, or one after a restart that kept the ledger, owes nothing.
func TestMeterMachines_BillsMissedHoursOnce(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))

	launched := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	m := []*Machine{{Id: "gap", Size: "s", Tag: "hanzo-org:acme", CreatedTime: launched.Format(time.RFC3339)}}
	if id := LaunchCharge("gap", launched); id != "compute-gap-2026070215" {
		t.Fatalf("launch charge = %q", id)
	}
	MarkBilled("gap", launched)                                     // the launch's debit landed
	meterMachines(context.Background(), m, launched.Add(time.Hour)) // 16:05
	at := time.Date(2026, 7, 2, 20, 10, 0, 0, time.UTC)
	if metered, _ := meterMachines(context.Background(), m, at); metered != 4 {
		t.Fatalf("the sweep after a four-hour gap billed %d hours, want 4", metered)
	}
	if metered, _ := meterMachines(context.Background(), m, at.Add(30*time.Minute)); metered != 0 {
		t.Fatalf("a second sweep in the same hour billed %d hours", metered)
	}

	mu.Lock()
	defer mu.Unlock()
	var ids []string
	for _, r := range *recs {
		ids = append(ids, r.usage.ID)
	}
	want := []string{"compute-gap-2026070216", "compute-gap-2026070217", "compute-gap-2026070218", "compute-gap-2026070219", "compute-gap-2026070220"}
	if !slices.Equal(ids, want) {
		t.Fatalf("debits = %v, want %v — each hour once, the launch hour not again", ids, want)
	}
}

// A machine owes nothing for the hours it was stopped: CreatedTime is its last
// start, and billing resumes there, not after the last hour it was billed.
func TestMeterMachines_OwesNothingForStoppedHours(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))

	MarkBilled("nap", time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)) // the launch's debit landed
	restarted := time.Date(2026, 7, 2, 14, 20, 0, 0, time.UTC)
	if id := startCharge("nap", restarted); id != "compute-nap-2026070214" {
		t.Fatalf("start charge = %q", id)
	}
	MarkBilled("nap", restarted) // and the start's
	m := []*Machine{{Id: "nap", Size: "s", Tag: "hanzo-org:acme", CreatedTime: restarted.Format(time.RFC3339)}}
	meterMachines(context.Background(), m, time.Date(2026, 7, 2, 15, 1, 0, 0, time.UTC))
	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 1 || (*recs)[0].usage.ID != "compute-nap-2026070215" {
		t.Fatalf("debits = %+v, want only hour 15", *recs)
	}
}

// A start in an hour the launch already billed charges nothing, and a start in a
// later hour charges that hour once — once its debit has landed. A start whose
// debit did not land leaves its hour owed.
func TestAStartChargesOnlyAnUnbilledHour(t *testing.T) {
	fakeCommerce(t)
	at := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	if id := LaunchCharge("twice", at); id != "compute-twice-2026070215" {
		t.Fatalf("launch charge = %q", id)
	}
	if id := startCharge("twice", at); id == "" {
		t.Fatal("an hour whose launch debit has not landed reads as billed")
	}
	MarkBilled("twice", at)
	if id := startCharge("twice", at.Add(40*time.Minute)); id != "" {
		t.Fatalf("a start in the launch hour charges %q", id)
	}
	later := at.Add(2 * time.Hour)
	if id := startCharge("twice", later); id != "compute-twice-2026070217" {
		t.Fatalf("a later start charges %q", id)
	}
	if id := startCharge("twice", later.Add(time.Minute)); id != "compute-twice-2026070217" {
		t.Fatalf("a start whose earlier debit did not land names %q, want the same hour's id again", id)
	}
	MarkBilled("twice", later)
	if id := startCharge("twice", later.Add(time.Minute)); id != "" {
		t.Fatalf("a second start in a billed hour charges %q", id)
	}
}

// The sweep must NOT re-bill the LAUNCH hour: the launch path already debited the
// machine one hour at create time. A machine created within the current sweep hour
// is skipped for that hour; the NEXT hour it is metered normally.
func TestMeterMachines_SkipsLaunchHour(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))

	// Launched at 15:05, its launch debit landed; the sweep fires later in the
	// SAME clock hour (15:40).
	launched := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	m := []*Machine{{Id: "777", Size: "s", Tag: "hanzo-org:acme", CreatedTime: launched.Format(time.RFC3339)}}
	MarkBilled("777", launched)

	// Same hour as launch -> skipped (launch already billed hour 15).
	metered, _ := meterMachines(context.Background(), m, launched.Add(35*time.Minute))
	if metered != 0 {
		t.Fatalf("launch-hour sweep metered=%d, want 0 (launch already billed this hour)", metered)
	}
	// Next hour -> billed once (the first RECURRING hour).
	metered, _ = meterMachines(context.Background(), m, launched.Add(time.Hour))
	if metered != 1 {
		t.Fatalf("next-hour sweep metered=%d, want 1", metered)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 1 {
		t.Fatalf("recorded %d debits, want 1 (launch hour skipped, next hour billed)", len(*recs))
	}
	if id := (*recs)[0].usage.ID; id != "compute-777-2026070216" {
		t.Fatalf("id = %q, want compute-777-2026070216 (next hour)", id)
	}
}

func TestCreatedInHour(t *testing.T) {
	stamp := "2026070215"
	if !CreatedInHour("2026-07-02T15:05:00Z", stamp) {
		t.Fatal("same-hour create time must be in-hour")
	}
	if CreatedInHour("2026-07-02T14:59:00Z", stamp) {
		t.Fatal("previous-hour create time must NOT be in-hour")
	}
	if CreatedInHour("2026-07-02T16:00:00Z", stamp) {
		t.Fatal("next-hour create time must NOT be in-hour")
	}
	// Empty / unparseable -> never in-hour (metered normally, never wrongly skipped).
	if CreatedInHour("", stamp) {
		t.Fatal("empty create time must not skip metering")
	}
	if CreatedInHour("not-a-time", stamp) {
		t.Fatal("unparseable create time must not skip metering")
	}
}

// An untagged machine (no hanzo-org tag) is skipped, never billed to a wrong
// tenant; a machine whose price cannot be RESOLVED is skipped and counted as
// skipped, whether the slug is missing from the catalog or the catalog priced it
// at zero.
//
// The zero case is the one worth stating: the upstream publishes no $0 size, so a
// zero is an unresolved price wearing a free-tier costume. This used to `continue`
// without incrementing skipped — "a $0 size is free" — which meant a machine
// running on an unpriced slug was invisible in both the ledger AND the sweep
// counters. Now it is a loud skip, and the count says so.
func TestMeterMachines_SkipsUnattributableAndUnpriceable(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t,
		priced("paid", 5),
		priced("zero", 0),
	)

	now := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	machines := []*Machine{
		{Id: "1", Size: "paid", Tag: "foo,bar"},           // no org tag -> skip
		{Id: "2", Size: "unknown", Tag: "hanzo-org:acme"}, // not in catalog -> skip
		{Id: "3", Size: "zero", Tag: "hanzo-org:acme"},    // priced at zero -> skip, never a $0 debit
		{Id: "4", Size: "paid", Tag: "hanzo-org:acme"},    // the only billable one
	}
	metered, skipped := meterMachines(context.Background(), machines, now)
	if metered != 1 {
		t.Fatalf("metered = %d, want 1 (only the paid+tagged machine)", metered)
	}
	if skipped != 3 { // untagged + not-in-catalog + priced-at-zero
		t.Fatalf("skipped = %d, want 3", skipped)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 1 {
		t.Fatalf("recorded %d debits, want 1", len(*recs))
	}
	if (*recs)[0].org != "acme" {
		t.Fatalf("billed org %q, want acme", (*recs)[0].org)
	}
}

// Two orgs' running machines are billed to their OWN ledgers — no cross-tenant
// fold in a mixed sweep.
func TestMeterMachines_TenantIsolation(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))

	now := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	machines := []*Machine{
		{Id: "a1", Size: "s", Tag: "hanzo-org:acme"},
		{Id: "m1", Size: "s", Tag: "hanzo-org:maxpower"},
	}
	metered, _ := meterMachines(context.Background(), machines, now)
	if metered != 2 {
		t.Fatalf("metered = %d, want 2", metered)
	}
	mu.Lock()
	defer mu.Unlock()
	byOrg := map[string]int{}
	for _, r := range *recs {
		byOrg[r.org]++
	}
	if byOrg["acme"] != 1 || byOrg["maxpower"] != 1 {
		t.Fatalf("per-org debits = %v, want one each for acme and maxpower", byOrg)
	}
}

// No IAM identity makes the whole sweep a no-op: MeteringConfigured() is false,
// so MeterRunningMachines returns before enumerating or debiting anything — an
// unconfigured deployment is never billed or spammed with failed (401) debits.
func TestMetering_UnconfiguredIsNoop(t *testing.T) {
	t.Setenv("iamEndpoint", "https://iam.example")
	t.Setenv("clientId", "hanzo-visor")
	t.Setenv("clientSecret", "")
	if MeteringConfigured() {
		t.Fatal("MeteringConfigured() = true with no client secret, want false")
	}
	t.Setenv("clientSecret", "shh")
	if !MeteringConfigured() {
		t.Fatal("MeteringConfigured() = false with a full identity, want true")
	}
}

// A launch whose debit never landed recorded nothing, so the sweep bills its
// hour: the ledger holds only hours commerce charged.
func TestMeterMachines_BillsALaunchHourWhoseDebitFailed(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))
	launched := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	LaunchCharge("lost", launched) // named, never debited
	m := []*Machine{{Id: "lost", Size: "s", Tag: "hanzo-org:acme", CreatedTime: launched.Format(time.RFC3339)}}
	meterMachines(context.Background(), m, launched.Add(30*time.Minute))
	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 1 || (*recs)[0].usage.ID != "compute-lost-2026070215" {
		t.Fatalf("debits = %+v, want the launch hour", *recs)
	}
}

// fundedAt is a commerce whose orgs hold the balances given, which fall as
// debits land; an org absent from the map is unreadable.
func fundedAt(t *testing.T, balances map[string]int64) (debits func() []string) {
	t.Helper()
	freshLedger(t)
	var mu sync.Mutex
	var ids []string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		org := r.Header.Get("X-Org-Id")
		have, ok := balances[org]
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"available":`+strconv.FormatInt(have, 10)+`,"account":"`+org+`"}`)
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		u := commercetest.Read(r)
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, u.ID)
		if have, ok := balances[u.Org]; ok {
			balances[u.Org] = have - u.Cents()
		}
		_, _ = io.WriteString(w, `{"id":"`+u.ID+`"}`)
	})
	commercetest.Serve(t, mux)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ids...)
	}
}

// stops records which machines the sweep stopped, instead of EC2.
func stops(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	saved := suspend
	suspend = func(_ context.Context, org, id string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, org+"/"+id)
		return nil
	}
	t.Cleanup(func() { suspend = saved })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// The hour now starting is paid for as it starts. An org that cannot pay for it
// has the machine stopped and that hour is not charged; the hours the machine
// already ran are charged whatever the balance, and so is a stopped machine's
// disk. Within one org, the balance pays machines in turn until it cannot.
func TestAMachineItsOrgCannotPayForIsStopped(t *testing.T) {
	debits := fundedAt(t, map[string]int64{"acme": 12, "beta": 0})
	stopped := stops(t)
	seedCatalog(t, offer{slug: "s", listMicros: 37_500 - ipv4MicrosPerHour, diskGB: 50})

	at := func(h int) time.Time { return time.Date(2026, 7, 2, h, 10, 0, 0, time.UTC) }
	started := at(12).Format(time.RFC3339)
	for _, id := range []string{"a1", "a2", "b1"} {
		MarkBilled(id, at(12))
	}
	MarkBilled("b2", at(12))
	machines := []*Machine{
		// Each owes 13, which ran, and 14, which is starting; 6c an hour.
		{Id: "a1", Size: "s", Tag: "hanzo-org:acme", State: "Running", CreatedTime: started},
		{Id: "a2", Size: "s", Tag: "hanzo-org:acme", State: "Running", CreatedTime: started},
		{Id: "b1", Size: "s", Tag: "hanzo-org:beta", State: "Running", CreatedTime: started},
		{Id: "b2", Size: "s", Tag: "hanzo-org:beta", State: "Stopped", CreatedTime: started},
	}
	meterMachines(context.Background(), machines, at(14))

	// acme had 12: hours 13 of a1 and a2 take it to 0 — the past is charged —
	// and neither can pay for 14. beta had nothing and pays its past too.
	want := []string{
		"compute-a1-2026070213", "compute-a2-2026070213", "compute-b1-2026070213",
		"compute-b2-disk-2026070213", "compute-b2-disk-2026070214",
	}
	if got := debits(); !slices.Equal(got, want) {
		t.Fatalf("debits = %v, want %v", got, want)
	}
	if got := stopped(); !slices.Equal(got, []string{"acme/a1", "acme/a2", "beta/b1"}) {
		t.Fatalf("stopped = %v", got)
	}
	// Hour 14 was not charged, so it is not recorded as billed.
	if mark, _ := billed().Through("a1"); mark.Hour != "2026070213" {
		t.Fatalf("a1 is billed through %+v", mark)
	}
}

// Enough for one machine's hour and not two: the first is charged, the second
// is stopped.
func TestABalancePaysMachinesInTurn(t *testing.T) {
	debits := fundedAt(t, map[string]int64{"acme": 8})
	stopped := stops(t)
	seedCatalog(t, offer{slug: "s", listMicros: 37_500 - ipv4MicrosPerHour, diskGB: 50})
	at := time.Date(2026, 7, 2, 14, 10, 0, 0, time.UTC)
	started := at.Add(-2 * time.Hour).Format(time.RFC3339)
	MarkBilled("x1", at.Add(-time.Hour))
	MarkBilled("x2", at.Add(-time.Hour))
	meterMachines(context.Background(), []*Machine{
		{Id: "x1", Size: "s", Tag: "hanzo-org:acme", State: "Running", CreatedTime: started},
		{Id: "x2", Size: "s", Tag: "hanzo-org:acme", State: "Running", CreatedTime: started},
	}, at)
	if got := debits(); !slices.Equal(got, []string{"compute-x1-2026070214"}) {
		t.Fatalf("debits = %v", got)
	}
	if got := stopped(); !slices.Equal(got, []string{"acme/x2"}) {
		t.Fatalf("stopped = %v", got)
	}
}

// A balance that cannot be read stops nothing: the hour is charged, and the next
// sweep asks again.
func TestAnUnreadableBalanceStopsNothing(t *testing.T) {
	debits := fundedAt(t, map[string]int64{})
	stopped := stops(t)
	seedCatalog(t, priced("s", 5))
	at := time.Date(2026, 7, 2, 14, 10, 0, 0, time.UTC)
	MarkBilled("u1", at.Add(-time.Hour))
	meterMachines(context.Background(), []*Machine{
		{Id: "u1", Size: "s", Tag: "hanzo-org:acme", State: "Running", CreatedTime: at.Add(-2 * time.Hour).Format(time.RFC3339)},
	}, at)
	if got := debits(); !slices.Equal(got, []string{"compute-u1-2026070214"}) {
		t.Fatalf("debits = %v", got)
	}
	if got := stopped(); len(got) != 0 {
		t.Fatalf("stopped = %v", got)
	}
}

// sends stands in for CloudWatch: bytes by instance and hour.
func sends(t *testing.T, sent map[string]map[string]int64, fail error) {
	t.Helper()
	saved := outbound
	outbound = func(_ context.Context, instances []string, from, to time.Time) (map[string]map[string]int64, error) {
		if fail != nil {
			return nil, fail
		}
		return sent, nil
	}
	t.Cleanup(func() { outbound = saved })
}

// A transfer hour whose debit fails is owed again, with the carry it had, and an
// unreadable NetworkOut bills nothing and leaves every hour owed.
func TestTransferThatWasNotChargedIsStillOwed(t *testing.T) {
	var up bool
	var ids []string
	freshLedger(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		u := commercetest.Read(r)
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ids = append(ids, u.ID+"="+u.Amount.Decimal)
		_, _ = io.WriteString(w, `{"id":"`+u.ID+`"}`)
	})
	commercetest.Serve(t, mux)
	seedCatalog(t, priced("s", 5))

	at := func(h int) time.Time { return time.Date(2026, 7, 2, h, 20, 0, 0, time.UTC) }
	m := &Machine{Id: "tx", Size: "s", Tag: "hanzo-org:acme", State: "Stopped", CreatedTime: at(10).Format(time.RFC3339), instance: "i-tx"}
	MarkBilled("tx", at(10))
	MarkBilled(diskKey("tx"), at(20))
	sends(t, map[string]map[string]int64{"i-tx": {"2026070210": gbBytes, "2026070211": 2 * gbBytes}}, nil)

	meterMachines(context.Background(), []*Machine{m}, at(12)) // commerce down: 10 and 11 stay owed
	if mark, _ := billed().Through(transferKey("tx")); mark.Hour != "" {
		t.Fatalf("a refused transfer debit was recorded: %+v", mark)
	}
	sends(t, nil, errors.New("cloudwatch unreachable"))
	up = true
	meterMachines(context.Background(), []*Machine{m}, at(13)) // metric unreadable: still owed
	if len(ids) != 0 {
		t.Fatalf("an unreadable metric charged %v", ids)
	}
	sends(t, map[string]map[string]int64{"i-tx": {"2026070210": gbBytes, "2026070211": 2 * gbBytes}}, nil)
	meterMachines(context.Background(), []*Machine{m}, at(14))
	want := []string{"compute-tx-transfer-2026070210=0.12", "compute-tx-transfer-2026070211=0.24"}
	if !slices.Equal(ids, want) {
		t.Fatalf("debits = %v, want %v", ids, want)
	}
}
