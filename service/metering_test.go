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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPriceToCents(t *testing.T) {
	cases := []struct {
		price float64
		want  int64
	}{
		{0.07, 7},     // whole-cent: float overshoot must NOT ceil to 8
		{0.29, 29},    // whole-cent
		{33.60, 3360}, // monthly-scale whole cents
		{4.2375, 424}, // H100/hr -> 423.75 -> 424
		{0.04999, 5},  // 4.999 -> 5
		{0.00833, 1},  // true sub-cent still charges >= 1
		{0.0, 0},      // free -> 0 (no charge)
		{-1.0, 0},     // guard: negative -> 0
	}
	for _, c := range cases {
		if got := PriceToCents(c.price); got != c.want {
			t.Fatalf("PriceToCents(%v) = %d, want %d", c.price, got, c.want)
		}
	}
}

func TestOrgFromTag(t *testing.T) {
	cases := []struct {
		tags string
		want string
	}{
		{"hanzo-org:acme,", "acme"},
		{"foo,hanzo-org:maxpower,bar", "maxpower"},
		{" hanzo-org:acme ", "acme"},
		{"foo,bar", ""},           // no org tag -> unattributable
		{"", ""},                  // empty
		{"hanzo-orgx:acme", ""},   // must be the exact key, not a prefix collision
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
		{"hanzo-org:acme,", ""},          // org only -> default project
		{"", ""},                         // empty -> default project
		{"hanzo-projectx:web", ""},       // exact key, not a prefix collision
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

// A running machine carrying a project tag attributes its debit to org/project via
// the Actor, while the debit destination (User + X-Org-Id) stays the org — one org
// balance covers all its projects.
func TestMeterMachines_AttributesProjectViaActor(t *testing.T) {
	url, recs, mu := fakeCommerce(t)
	t.Setenv("COMMERCE_URL", url)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	seedCatalog(t, SizeInfo{Slug: "s", PriceHourly: 0.05, Currency: "USD"})

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
	var u struct {
		User  string `json:"user"`
		Actor string `json:"actor"`
	}
	if err := json.Unmarshal(r.body, &u); err != nil {
		t.Fatalf("parse usage: %v", err)
	}
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want acme (org is the billing key)", u.User)
	}
	if u.Actor != "acme/web" {
		t.Fatalf("actor = %q, want acme/web (per-project attribution)", u.Actor)
	}
}

func TestHourStamp_IdempotencyBucket(t *testing.T) {
	base := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	// Same hour -> same stamp (debit dedups).
	if a, b := hourStamp(base), hourStamp(base.Add(59*time.Minute)); a != b {
		t.Fatalf("same-hour stamps differ: %q vs %q", a, b)
	}
	// Next hour -> different stamp (a new hour is a new billable unit).
	if a, b := hourStamp(base), hourStamp(base.Add(time.Hour)); a == b {
		t.Fatalf("cross-hour stamps equal: %q", a)
	}
	if got := hourStamp(base); got != "2026070215" {
		t.Fatalf("hourStamp = %q, want 2026070215", got)
	}
}

// recorded is one usage debit captured by the fake commerce.
type recorded struct {
	org  string
	body []byte
}

// fakeCommerce records every usage POST; it never gates (Record does not check
// balance). Returns the base URL.
func fakeCommerce(t *testing.T) (url string, got *[]recorded, mu *sync.Mutex) {
	t.Helper()
	var recs []recorded
	var m sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m.Lock()
		recs = append(recs, recorded{org: r.Header.Get("X-Org-Id"), body: b})
		m.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &recs, &m
}

// seedCatalog pre-populates the package catalog cache so SizeBySlug resolves
// without a live DigitalOcean call.
func seedCatalog(t *testing.T, sizes ...SizeInfo) {
	t.Helper()
	catalog.mu.Lock()
	catalog.sizes = sizes
	catalog.fetchedAt = time.Now()
	catalog.mu.Unlock()
	t.Cleanup(func() {
		catalog.mu.Lock()
		catalog.sizes = nil
		catalog.fetchedAt = time.Time{}
		catalog.mu.Unlock()
	})
}

func parseUsage(t *testing.T, b []byte) (user, provider, model string, amount int64, reqID string) {
	t.Helper()
	var u struct {
		User      string `json:"user"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Amount    int64  `json:"amount"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatalf("parse usage %q: %v", b, err)
	}
	return u.User, u.Provider, u.Model, u.Amount, u.RequestID
}

// A running machine debits its OWNING org one hour of resale price, attributed
// product "compute" / model <size>, with an hour-bucketed idempotency RequestID.
func TestMeterMachines_DebitsRunningMachinePerOrg(t *testing.T) {
	url, recs, mu := fakeCommerce(t)
	t.Setenv("COMMERCE_URL", url)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	seedCatalog(t, SizeInfo{Slug: "s-2vcpu-4gb", PriceHourly: 0.05, Currency: "USD"})

	now := time.Date(2026, 7, 2, 15, 30, 0, 0, time.UTC)
	machines := []*Machine{{Id: "111", Size: "s-2vcpu-4gb", Tag: "hanzo-org:acme,"}}

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
	user, provider, model, amount, reqID := parseUsage(t, r.body)
	if user != "acme" {
		t.Fatalf("debit user = %q, want acme", user)
	}
	if provider != "compute" {
		t.Fatalf("provider = %q, want compute", provider)
	}
	if model != "s-2vcpu-4gb" {
		t.Fatalf("model = %q, want s-2vcpu-4gb", model)
	}
	if amount != 5 { // $0.05 -> 5 cents
		t.Fatalf("amount = %d, want 5", amount)
	}
	if reqID != "compute-111-2026070215" {
		t.Fatalf("requestId = %q, want compute-111-2026070215 (hour-bucketed idempotency)", reqID)
	}
}

// The idempotency key is stable across two sweeps in the SAME hour (commerce
// dedups), and changes in the NEXT hour (a new billable unit).
func TestMeterMachines_IdempotentWithinHour(t *testing.T) {
	url, recs, mu := fakeCommerce(t)
	t.Setenv("COMMERCE_URL", url)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	seedCatalog(t, SizeInfo{Slug: "s-1vcpu-1gb", PriceHourly: 0.01, Currency: "USD"})

	m := []*Machine{{Id: "222", Size: "s-1vcpu-1gb", Tag: "hanzo-org:acme"}}
	h15 := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	meterMachines(context.Background(), m, h15)
	meterMachines(context.Background(), m, h15.Add(50*time.Minute)) // same hour
	meterMachines(context.Background(), m, h15.Add(time.Hour))      // next hour

	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 3 {
		t.Fatalf("recorded %d, want 3 (commerce dedups by requestId, not us)", len(*recs))
	}
	_, _, _, _, id1 := parseUsage(t, (*recs)[0].body)
	_, _, _, _, id2 := parseUsage(t, (*recs)[1].body)
	_, _, _, _, id3 := parseUsage(t, (*recs)[2].body)
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
	url, recs, mu := fakeCommerce(t)
	t.Setenv("COMMERCE_URL", url)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	seedCatalog(t, SizeInfo{Slug: "s", PriceHourly: 0.05, Currency: "USD"})

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
	_, _, _, _, reqID := parseUsage(t, (*recs)[0].body)
	if reqID != "compute-777-2026070216" {
		t.Fatalf("requestId = %q, want compute-777-2026070216 (next hour)", reqID)
	}
}

func TestCreatedInHour(t *testing.T) {
	stamp := "2026070215"
	if !createdInHour("2026-07-02T15:05:00Z", stamp) {
		t.Fatal("same-hour create time must be in-hour")
	}
	if createdInHour("2026-07-02T14:59:00Z", stamp) {
		t.Fatal("previous-hour create time must NOT be in-hour")
	}
	if createdInHour("2026-07-02T16:00:00Z", stamp) {
		t.Fatal("next-hour create time must NOT be in-hour")
	}
	// Empty / unparseable -> never in-hour (metered normally, never wrongly skipped).
	if createdInHour("", stamp) {
		t.Fatal("empty create time must not skip metering")
	}
	if createdInHour("not-a-time", stamp) {
		t.Fatal("unparseable create time must not skip metering")
	}
}

// An untagged machine (no hanzo-org tag) is skipped, never billed to a wrong
// tenant; an unknown-size machine is skipped; a $0 size is not debited.
func TestMeterMachines_SkipsUnattributableUnknownAndFree(t *testing.T) {
	url, recs, mu := fakeCommerce(t)
	t.Setenv("COMMERCE_URL", url)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	seedCatalog(t,
		SizeInfo{Slug: "paid", PriceHourly: 0.05, Currency: "USD"},
		SizeInfo{Slug: "free", PriceHourly: 0.0, Currency: "USD"},
	)

	now := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	machines := []*Machine{
		{Id: "1", Size: "paid", Tag: "foo,bar"},           // no org tag -> skip
		{Id: "2", Size: "unknown", Tag: "hanzo-org:acme"}, // size not in catalog -> skip
		{Id: "3", Size: "free", Tag: "hanzo-org:acme"},    // $0 -> not debited (not skipped-count)
		{Id: "4", Size: "paid", Tag: "hanzo-org:acme"},    // the only billable one
	}
	metered, skipped := meterMachines(context.Background(), machines, now)
	if metered != 1 {
		t.Fatalf("metered = %d, want 1 (only the paid+tagged machine)", metered)
	}
	if skipped != 2 { // untagged + unknown-size (free is neither metered nor skipped)
		t.Fatalf("skipped = %d, want 2", skipped)
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
	url, recs, mu := fakeCommerce(t)
	t.Setenv("COMMERCE_URL", url)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	seedCatalog(t, SizeInfo{Slug: "s", PriceHourly: 0.05, Currency: "USD"})

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

// Unconfigured commerce (no service token) makes the whole sweep a no-op:
// MeteringConfigured() is false, so MeterRunningMachines returns before
// enumerating or debiting anything — an unconfigured deployment is never billed
// or spammed with failed (401) debits. This is the operator-facing gate; the
// token is the KMS-provisioned secret the launch path also requires.
func TestMetering_UnconfiguredIsNoop(t *testing.T) {
	t.Setenv("COMMERCE_URL", "http://commerce.invalid")
	t.Setenv("COMMERCE_SERVICE_TOKEN", "") // no token -> not configured
	if MeteringConfigured() {
		t.Fatal("MeteringConfigured() = true with no COMMERCE_SERVICE_TOKEN, want false")
	}
	// With a token present it reports configured (base URL always defaults).
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	if !MeteringConfigured() {
		t.Fatal("MeteringConfigured() = false with a token, want true")
	}
}
