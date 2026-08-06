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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/digitalocean/godo"
)

// ---- fake commerce, by verdict ----

// commerceMood is how the fake commerce answers a balance read. Each value is a
// real production failure: the org is broke, the KMS-synced service token was
// rotated out from under us, commerce is having a bad day, commerce is wedged.
type commerceMood int

const (
	funded commerceMood = iota
	unfunded
	unauthorized // 401 — absent or rotated COMMERCE_SERVICE_TOKEN
	broken       // 500
	hanging      // no answer before the client's own timeout
)

// commerceOf stands up a fake commerce in one mood and points the metering
// client at it. It counts balance reads and usage writes, so a test can assert
// not only that a provision was refused but that nothing was billed for it.
func commerceOf(t *testing.T, mood commerceMood, availableCents int64) (debits *int, mu *sync.Mutex) {
	t.Helper()
	var n int
	var m sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		switch mood {
		case unauthorized:
			w.WriteHeader(http.StatusUnauthorized)
		case broken:
			w.WriteHeader(http.StatusInternalServerError)
		case hanging:
			// Outlast the client's 5s timeout without ever answering.
			<-r.Context().Done()
		case unfunded:
			_ = json.NewEncoder(w).Encode(map[string]any{"available": 0, "currency": "usd"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"available": availableCents, "currency": "usd"})
		}
	})
	// Funded callers go on to the scope-cap check; it fails open by contract, so
	// an unrouted 404 here is a legitimate "no cap configured".
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		m.Lock()
		n++
		m.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"transactionId":"tx","type":"usage"}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("COMMERCE_URL", srv.URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	return &n, &m
}

// ---- the price half of the gate ----

// A GPU slug in the catalog resolves to its resale rate. This is the headline:
// the price table that used to answer this question had 32 hardcoded droplet
// slugs and not one GPU, so every GPU resource priced at zero.
func TestHourlyCents_ResolvesGPU(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD",
		GPU: &GPUSpec{Count: 8, Model: "H100"}})

	cents, err := HourlyCents("gpu-h100x8-640gb")
	if err != nil {
		t.Fatalf("a catalog GPU size must price, got %v", err)
	}
	if cents != 3178 { // ceil(3177.24)
		t.Fatalf("HourlyCents = %d, want 3178", cents)
	}
}

// An unresolvable price is an ERROR, never a zero. Both shapes count: a slug the
// catalog does not carry, and a slug the catalog prices at zero. Billing either
// one as free is how an H100 runs for nothing.
func TestHourlyCents_RefusesRatherThanZero(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "zero", PriceHourly: 0, Currency: "USD"})

	for _, slug := range []string{"gpu-h100x8-640gb", "zero", ""} {
		cents, err := HourlyCents(slug)
		if err == nil {
			t.Fatalf("HourlyCents(%q) must refuse, got %d cents", slug, cents)
		}
		if !errors.Is(err, ErrPriceUnavailable) {
			t.Fatalf("HourlyCents(%q) error must be ErrPriceUnavailable, got %v", slug, err)
		}
		if cents != 0 {
			t.Fatalf("HourlyCents(%q) must not return a usable price alongside a refusal", slug)
		}
	}
}

// ---- the balance half of the gate ----

// Every way commerce can fail to say YES is a refusal. This is the mutation
// target: flip AuthorizeCompute to allow-on-error and the four failure moods
// stop refusing, so this test fails.
func TestAuthorizeCompute_FailsClosed(t *testing.T) {
	for name, mood := range map[string]commerceMood{
		"unfunded":     unfunded,
		"401 rotated":  unauthorized,
		"500 upstream": broken,
		"timeout":      hanging,
	} {
		t.Run(name, func(t *testing.T) {
			commerceOf(t, mood, 0)
			if err := AuthorizeCompute(context.Background(), "acme", "", 3178); err == nil {
				t.Fatal("commerce did not say yes, so the gate must refuse")
			}
		})
	}
}

// A funded org is NOT refused. A gate that denies everyone is not fail-closed,
// it is broken, and this is the test that tells the two apart.
func TestAuthorizeCompute_AllowsFundedOrg(t *testing.T) {
	commerceOf(t, funded, 100000)
	if err := AuthorizeCompute(context.Background(), "acme", "", 3178); err != nil {
		t.Fatalf("a funded org must be authorized, got %v", err)
	}
}

// The gate is for the FULL first interval, not a token amount: a balance that
// covers one node of an eight-GPU pool does not buy the pool.
func TestAuthorizeCompute_GatesTheWholeAmount(t *testing.T) {
	commerceOf(t, funded, 3178) // exactly one node-hour
	if err := AuthorizeCompute(context.Background(), "acme", "", 3178); err != nil {
		t.Fatalf("balance covers one hour, must be allowed: %v", err)
	}
	if err := AuthorizeCompute(context.Background(), "acme", "", 3178*4); err == nil {
		t.Fatal("balance covers one hour but four were requested — must refuse")
	}
}

// A zero charge is never authorized. Without this, an unresolved price that
// slipped past HourlyCents would reach the gate as "authorize $0", which every
// balance on earth can afford.
func TestAuthorizeCompute_RefusesZeroCharge(t *testing.T) {
	commerceOf(t, funded, 100000)
	if err := AuthorizeCompute(context.Background(), "acme", "", 0); err == nil {
		t.Fatal("a zero charge must be refused, not waved through")
	}
}

// ---- the composition: refused means NOTHING was provisioned ----

// The whole point of a pre-provision gate is that a refusal costs nothing. For
// every way commerce can fail, and for an unpriceable size, the upstream must
// never receive a create request — asserted against the fake DigitalOcean, which
// records the create it was asked for.
func TestCreateClusterMetered_ProvisionsNothingWhenRefused(t *testing.T) {
	spec := &CreateClusterSpec{Name: "acme-gpu", Region: "nyc3",
		NodePool: CreateClusterNodePool{Size: "gpu-h100x8-640gb", Count: 4}}

	cases := map[string]struct {
		mood commerceMood
		size string
	}{
		"unfunded":         {unfunded, "gpu-h100x8-640gb"},
		"401 rotated":      {unauthorized, "gpu-h100x8-640gb"},
		"500 upstream":     {broken, "gpu-h100x8-640gb"},
		"timeout":          {hanging, "gpu-h100x8-640gb"},
		"price unresolved": {funded, "gpu-h200x8-1128gb"}, // funded, but not in the catalog
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD"})
			debits, mu := commerceOf(t, c.mood, 0)
			do := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}}
			client := newDOKSTestClient(t, do)

			s := *spec
			s.NodePool.Size = c.size
			cluster, err := createClusterMetered(context.Background(), client, "acme", "", &s)

			if err == nil {
				t.Fatal("the provision must be refused")
			}
			if cluster != nil {
				t.Fatalf("a refused provision must return no cluster, got %+v", cluster)
			}
			if do.created != nil {
				t.Fatalf("REFUSED BUT PROVISIONED: upstream received %+v", do.created)
			}
			mu.Lock()
			defer mu.Unlock()
			if *debits != 0 {
				t.Fatalf("a refused provision must bill nothing, got %d debits", *debits)
			}
		})
	}
}

// The legit path is not broken by the gate: a funded org gets its GPU cluster,
// the upstream receives exactly the requested pool, and the org is billed the
// REAL rate for all four nodes — not zero.
func TestCreateClusterMetered_FundedOrgLaunchesAndPaysTheRealRate(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD"})
	debits, mu := commerceOf(t, funded, 1000000)
	do := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}}
	client := newDOKSTestClient(t, do)

	cluster, err := createClusterMetered(context.Background(), client, "acme", "", &CreateClusterSpec{
		Name: "acme-gpu", Region: "nyc3",
		NodePool: CreateClusterNodePool{Size: "gpu-h100x8-640gb", Count: 4},
	})
	if err != nil {
		t.Fatalf("a funded org must be able to launch: %v", err)
	}
	if cluster == nil || do.created == nil {
		t.Fatal("the funded path must actually provision")
	}
	if do.created.NodePools[0].Count != 4 || do.created.NodePools[0].Size != "gpu-h100x8-640gb" {
		t.Fatalf("upstream got the wrong pool: %+v", do.created.NodePools[0])
	}
	if !hasTag(do.created.Tags, "hanzo-org:acme") {
		t.Fatalf("the ownership tag must reach upstream: %v", do.created.Tags)
	}
	mu.Lock()
	defer mu.Unlock()
	if *debits != 1 {
		t.Fatalf("the launch hour must be billed exactly once, got %d debits", *debits)
	}
}

// The amount billed is the whole seed pool's hour (rate × nodes), and the count
// floor the upstream request applies is applied to the charge too — so the
// quantity authorized is always the quantity provisioned.
func TestCreateClusterMetered_BillsEveryNodeAndFloorsTheCount(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD"})

	for name, tc := range map[string]struct{ ask, want int }{
		"four nodes":       {4, 4},
		"zero floors to 1": {0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var billed int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/balance") {
					_ = json.NewEncoder(w).Encode(map[string]any{"available": 10000000, "currency": "usd"})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/usage") {
					var u struct {
						Amount   int64  `json:"amount"`
						Provider string `json:"provider"`
						Model    string `json:"model"`
					}
					_ = json.NewDecoder(r.Body).Decode(&u)
					billed = u.Amount
					if u.Provider != "compute" || u.Model != "gpu-h100x8-640gb" {
						t.Errorf("debit must name the compute plane and the size, got provider=%q model=%q", u.Provider, u.Model)
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"transactionId":"tx","type":"usage"}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			t.Cleanup(srv.Close)
			t.Setenv("COMMERCE_URL", srv.URL)
			t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")

			do := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}}
			if _, err := createClusterMetered(context.Background(), newDOKSTestClient(t, do), "acme", "",
				&CreateClusterSpec{Name: "g", Region: "nyc3",
					NodePool: CreateClusterNodePool{Size: "gpu-h100x8-640gb", Count: tc.ask}},
			); err != nil {
				t.Fatalf("funded launch: %v", err)
			}
			if want := int64(3178 * tc.want); billed != want {
				t.Fatalf("billed %d cents, want %d (3178 × %d nodes)", billed, want, tc.want)
			}
			if do.created.NodePools[0].Count != tc.want {
				t.Fatalf("provisioned %d nodes but authorized %d — the two must agree",
					do.created.NodePools[0].Count, tc.want)
			}
		})
	}
}

// ---- the sweep gate ----

// An absent service token means no sweep can debit, and saying so is the whole
// fix: the silent early-return this replaces let every running machine go
// unbilled with no log, no metric, and a healthy-looking service.
func TestBillable_IsFalseAndLoudWithoutAToken(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	if Billable(context.Background(), "compute.hourly") {
		t.Fatal("no service token means the sweep cannot debit")
	}
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	if !Billable(context.Background(), "compute.hourly") {
		t.Fatal("a wired token must let the sweep run")
	}
}

// The compute gate reads PREPAID balance, not the tier-aware effective balance
// that folds in promotional plan allotment: a free-tier grant buys inference, not
// an H100. Asserted on the wire — tier-aware would read /v1/billing/tier.
func TestAuthorizeCompute_ReadsPrepaidNotPromotionalCredit(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"available": 100000, "currency": "usd"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMMERCE_URL", srv.URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")

	if err := AuthorizeCompute(context.Background(), "acme", "", 3178); err != nil {
		t.Fatalf("funded: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if strings.Contains(p, "/tier") {
			t.Fatalf("compute must gate on prepaid balance, but it read %s", p)
		}
	}
	if len(paths) == 0 || !strings.Contains(paths[0], "/balance") {
		t.Fatalf("expected a prepaid balance read first, got %v", paths)
	}
}
