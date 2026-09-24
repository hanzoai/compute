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
	"io"
	"net/http"
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

// priced is an offer whose Hanzo price is exactly cents per hour: its list price
// is the cost that the fee turns into that many cents, with no disk.
func priced(slug string, cents int64) offer {
	return offer{slug: slug, listMicros: cents * microsPerCent * feeDen / feeNum}
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

// The usage id is stable across two sweeps in the SAME hour (the ledger debits
// one id once), and changes in the NEXT hour (a new billable unit).
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
	if len(*recs) != 3 {
		t.Fatalf("recorded %d, want 3 (the ledger dedups by id, not us)", len(*recs))
	}
	id1, id2, id3 := (*recs)[0].usage.ID, (*recs)[1].usage.ID, (*recs)[2].usage.ID
	if id1 != id2 {
		t.Fatalf("same-hour requestIds differ: %q vs %q (would let a sweep overlap double-bill)", id1, id2)
	}
	if id1 == id3 {
		t.Fatalf("cross-hour requestIds equal: %q (a new hour must be a new billable unit)", id1)
	}
}

// The sweep must NOT re-bill the LAUNCH hour: the launch path already debited the
// machine one hour at create time. A machine created within the current sweep hour
// is skipped for that hour; the NEXT hour it is metered normally.
func TestMeterMachines_SkipsLaunchHour(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, priced("s", 5))

	// Launched at 15:05; the sweep fires later in the SAME clock hour (15:40).
	launched := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	m := []*Machine{{Id: "777", Size: "s", Tag: "hanzo-org:acme", CreatedTime: launched.Format(time.RFC3339)}}

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

// A start in an hour the launch already billed charges nothing, and a start in a
// later hour charges that hour once.
func TestAStartChargesOnlyAnUnbilledHour(t *testing.T) {
	fakeCommerce(t)
	at := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	LaunchCharge("twice", at)
	if id := startCharge("twice", at.Add(40*time.Minute)); id != "" {
		t.Fatalf("a start in the launch hour charges %q", id)
	}
	if id := startCharge("twice", at.Add(2*time.Hour)); id != "compute-twice-2026070217" {
		t.Fatalf("a later start charges %q", id)
	}
	if id := startCharge("twice", at.Add(2*time.Hour+time.Minute)); id != "" {
		t.Fatalf("a second start in that hour charges %q", id)
	}
}
