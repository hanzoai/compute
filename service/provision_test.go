// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/digitalocean/godo"
)

// ledger is a commerce whose balance actually MOVES: the gate reads it, the debit
// spends it. A fake that answers a constant cannot tell a reservation from a
// read, which is exactly the distinction under test.
//
// It is keyed by X-Org-Id, like the real one — a shared pot would make one
// tenant's spending look like another's and turn every cross-org assertion into
// an artefact of the fake.
type ledger struct {
	mu        sync.Mutex
	available map[string]int64
	debits    map[string]int
}

func ledgerOf(t *testing.T, funded map[string]int64) *ledger {
	t.Helper()
	l := &ledger{available: map[string]int64{}, debits: map[string]int{}}
	for org, cents := range funded {
		l.available[org] = cents
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org := r.Header.Get("X-Org-Id")
		switch {
		case strings.HasSuffix(r.URL.Path, "/balance"):
			l.mu.Lock()
			a := l.available[org]
			l.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"available": a, "currency": "usd"})
		case strings.HasSuffix(r.URL.Path, "/usage"):
			var u struct {
				Amount int64 `json:"amount"`
			}
			_ = json.NewDecoder(r.Body).Decode(&u)
			l.mu.Lock()
			l.available[org] -= u.Amount
			l.debits[org]++
			l.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"transactionId":"tx","type":"usage"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMMERCE_URL", srv.URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	return l
}

func (l *ledger) state(org string) (available int64, debits int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.available[org], l.debits[org]
}

// An org funded for exactly ONE cluster-hour fires sixteen creates at once.
//
// AuthorizeCompute is a READ, and before the hold every one of those sixteen read
// the same balance in the window before any of them wrote a debit — measured at
// 16, 14 and 11 grants across three runs, finishing at minus $476 of provisioned
// GPU. The hold makes authorize→provision→record atomic per org, so the balance
// the first request spends is the balance the second one reads.
func TestProvisionSerialisesAnOrgSoOneBalanceBuysOneCluster(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD"})
	l := ledgerOf(t, map[string]int64{"acme": 3178}) // exactly one node-hour

	const n = 16
	clients := make([]*DOKSClient, n)
	dos := make([]*doksTestServer, n)
	for i := range clients {
		dos[i] = &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}}
		clients[i] = newDOKSTestClient(t, dos[i])
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := createClusterMetered(context.Background(), clients[i], newPoolLedger().record, "acme", "",
				&CreateClusterSpec{Name: "g", Region: "nyc3",
					NodePool: CreateClusterNodePool{Size: "gpu-h100x8-640gb", Count: 1}})
			if err == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	provisioned := 0
	for _, d := range dos {
		if d.created != nil {
			provisioned++
		}
	}
	available, debits := l.state("acme")
	t.Logf("balance funded ONE cluster-hour (3178c): %d/%d granted, %d provisioned, %d debits, balance now %d cents",
		granted, n, provisioned, debits, available)

	if granted != 1 {
		t.Fatalf("a one-cluster balance must buy exactly one cluster, %d of %d were granted", granted, n)
	}
	if provisioned != 1 {
		t.Fatalf("%d clusters reached DigitalOcean on a balance that funded 1", provisioned)
	}
	if debits != 1 || available != 0 {
		t.Fatalf("want exactly one debit taking the balance to 0, got %d debits and %d cents", debits, available)
	}
}

// Serialising must not deny a funded org: sixteen creates against sixteen
// cluster-hours all succeed. A gate that refuses everyone is not fail-closed,
// it is broken, and concurrency is where that is easiest to ship by accident.
func TestProvisionDoesNotRefuseAFundedOrgUnderLoad(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD"})
	const n = 16
	l := ledgerOf(t, map[string]int64{"acme": 3178 * n})

	clients := make([]*DOKSClient, n)
	for i := range clients {
		clients[i] = newDOKSTestClient(t, &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}})
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := createClusterMetered(context.Background(), clients[i], newPoolLedger().record, "acme", "",
				&CreateClusterSpec{Name: "g", Region: "nyc3",
					NodePool: CreateClusterNodePool{Size: "gpu-h100x8-640gb", Count: 1}}); err == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if granted != n {
		t.Fatalf("a fully funded org must get all %d clusters, got %d", n, granted)
	}
	if available, debits := l.state("acme"); debits != n || available != 0 {
		t.Fatalf("want %d debits taking the balance to 0, got %d debits and %d cents", n, debits, available)
	}
}

// The hold is per ORG: one tenant's provisions never serialise behind another's,
// so two orgs each funded for one cluster each get one.
func TestProvisionHoldsPerOrgNotGlobally(t *testing.T) {
	seedCatalog(t, SizeInfo{Slug: "gpu-h100x8-640gb", PriceHourly: 31.7724, Currency: "USD"})
	l := ledgerOf(t, map[string]int64{"acme": 3178, "globex": 3178})

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := map[string]int{}
	for _, org := range []string{"acme", "globex"} {
		for i := 0; i < 4; i++ {
			client := newDOKSTestClient(t, &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}})
			wg.Add(1)
			go func(org string) {
				defer wg.Done()
				if _, err := createClusterMetered(context.Background(), client, newPoolLedger().record, org, "",
					&CreateClusterSpec{Name: "g", Region: "nyc3",
						NodePool: CreateClusterNodePool{Size: "gpu-h100x8-640gb", Count: 1}}); err == nil {
					mu.Lock()
					granted[org]++
					mu.Unlock()
				}
			}(org)
		}
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for _, org := range []string{"acme", "globex"} {
		if granted[org] != 1 {
			t.Fatalf("each org funded for one cluster must get exactly one, got %v", granted)
		}
		if available, debits := l.state(org); debits != 1 || available != 0 {
			t.Fatalf("%s: want one debit taking its own balance to 0, got %d debits and %d cents", org, debits, available)
		}
	}
	if len(holds) != 0 {
		t.Fatalf("the hold map must be empty once every provision has left, got %d entries", len(holds))
	}
}

// A refused provision releases the hold, so the org is not wedged by its own
// insufficient balance.
func TestProvisionReleasesTheHoldOnRefusal(t *testing.T) {
	ledgerOf(t, map[string]int64{"acme": 0})

	for i := 0; i < 3; i++ {
		if err := Provision(context.Background(), "acme", "", 100, 100, "s", func() (string, error) {
			t.Fatal("a refused provision must not run the provision")
			return "", nil
		}); err == nil {
			t.Fatal("an unfunded org must be refused")
		}
	}
	if len(holds) != 0 {
		t.Fatalf("a refusal must release the hold, %d entries left", len(holds))
	}
}

// A provision that names nothing is a provision with no charge of its own — the
// scale path, whose added nodes the hourly sweep bills from its next hour.
func TestProvisionWithoutARequestIdDebitsNothing(t *testing.T) {
	l := ledgerOf(t, map[string]int64{"acme": 100000})

	if err := Provision(context.Background(), "acme", "", 3178, 3178, "gpu-h100x8-640gb", func() (string, error) {
		return "", nil
	}); err != nil {
		t.Fatalf("a funded provision must be allowed: %v", err)
	}
	if _, debits := l.state("acme"); debits != 0 {
		t.Fatalf("an unnamed provision must debit nothing, got %d", debits)
	}
}

// The gate holds a balance against a CEILING and charges what was actually
// taken. They are two numbers because they answer two questions, and the whole
// autoscale story rests on the difference: authorize sixteen node-hours, charge
// one, let the sweep charge the rest if and when the upstream adds them.
func TestProvisionAuthorizesTheCeilingAndDebitsTheActual(t *testing.T) {
	const ceiling, actual = 3178 * 16, 3178

	t.Run("an org good only for the actual cost cannot open the ceiling", func(t *testing.T) {
		l := ledgerOf(t, map[string]int64{"acme": actual})
		reached := false
		err := Provision(context.Background(), "acme", "", ceiling, actual, "gpu-h100x8-640gb",
			func() (string, error) { reached = true; return "p-1", nil })
		if err == nil {
			t.Fatal("the balance must cover the ceiling, not the opening charge")
		}
		if reached {
			t.Fatal("REFUSED BUT PROVISIONED")
		}
		if _, debits := l.state("acme"); debits != 0 {
			t.Fatalf("a refusal debits nothing, got %d", debits)
		}
	})

	t.Run("a funded org is charged the actual, never the ceiling", func(t *testing.T) {
		l := ledgerOf(t, map[string]int64{"acme": 100000000})
		if err := Provision(context.Background(), "acme", "", ceiling, actual, "gpu-h100x8-640gb",
			func() (string, error) { return "p-1", nil }); err != nil {
			t.Fatalf("a funded org must be allowed: %v", err)
		}
		available, debits := l.state("acme")
		if debits != 1 {
			t.Fatalf("want exactly one debit, got %d", debits)
		}
		if spent := 100000000 - available; spent != actual {
			t.Fatalf("spent %d cents, want %d (the ceiling would be %d)", spent, actual, ceiling)
		}
	})
}
