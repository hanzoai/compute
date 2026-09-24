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
	"sync"
	"testing"

	"github.com/hanzoai/compute/service/commercetest"
)

// ---- fake commerce, by verdict ----

// commerceMood is how the fake commerce answers a balance read. Each value is a
// real production failure: the org is broke, the KMS-synced service token was
// rotated out from under us, commerce is having a bad day, commerce is wedged.
type commerceMood int

const (
	funded commerceMood = iota
	unfunded
	unauthorized // 401 — a refused or revoked IAM identity
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

	commercetest.Serve(t, mux)
	return &n, &m
}

// ---- the price half of the gate ----

// A GPU size in the catalog resolves to its price: every GPU the hosted account
// sells is priced, and the price is the catalog's, to the cent.
func TestHourlyCents_ResolvesGPU(t *testing.T) {
	cents, err := HourlyCents("p4d.24xlarge")
	if err != nil {
		t.Fatalf("a catalog GPU size must price, got %v", err)
	}
	if cents != 2943 { // ($21.957642 + 1000 GB gp3) x 4/3, rounded up to the cent
		t.Fatalf("HourlyCents = %d, want 2943", cents)
	}
}

// An unresolvable price is an ERROR, never a zero. Both shapes count: a slug the
// catalog does not carry, and a slug the catalog prices at zero. Billing either
// one as free is how an H100 runs for nothing.
func TestHourlyCents_RefusesRatherThanZero(t *testing.T) {
	seedCatalog(t, priced("zero", 0))

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
