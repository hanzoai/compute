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
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/compute/service/commercetest"
)

// Every commerce call presents visor's own IAM token and names its org: the one
// credential, minted once and reused, and the tenant on every request.
func TestCommerceCallsPresentTheIAMTokenAndTheOrg(t *testing.T) {
	var mu sync.Mutex
	type seen struct{ method, path, auth, org string }
	var calls []seen
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Org-Id")})
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/billing/balance":
			commercetest.Balance(w, r, 10000)
		case "/v1/billing/alerts/authorize":
			_, _ = w.Write([]byte(`{"allow":true}`))
		case "/v1/billing/usage":
			u := commercetest.Read(r)
			if u.ID != "m-1" || u.Org != "acme" || u.Amount.Decimal != "31.78" || u.Amount.Currency != "usd" ||
				u.Service != "compute" || u.Model != "gpu-h100x1" || u.Project != "web" {
				t.Errorf("usage body does not speak the contract: %+v", u)
			}
			_, _ = w.Write([]byte(`{"id":"m-1","org":"acme","account":"acme","amount":{"decimal":"31.78","currency":"usd"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	if err := AuthorizeCompute(context.Background(), "acme", "web", 3178); err != nil {
		t.Fatalf("funded authorize: %v", err)
	}
	if err := RecordCompute(context.Background(), "acme", "web", 3178, "gpu-h100x1", "m-1"); err != nil {
		t.Fatalf("record: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"GET /v1/billing/balance", "GET /v1/billing/alerts/authorize", "POST /v1/billing/usage"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %v", calls, want)
	}
	for i, c := range calls {
		if c.method+" "+c.path != want[i] {
			t.Errorf("call %d = %s %s, want %s", i, c.method, c.path, want[i])
		}
		if c.auth != "Bearer "+commercetest.Token {
			t.Errorf("call %d presented %q, want visor's IAM token", i, c.auth)
		}
		if c.org != "acme" {
			t.Errorf("call %d named org %q, want acme", i, c.org)
		}
	}
}

// A balance answered for another account is visor's own org answering, because
// the token does not act for the org asked about. Gating a tenant's launch on it
// would admit work nobody can bill, so it refuses — and nothing is debited.
func TestAuthorizeRefusesABalanceAnsweredForAnotherOrg(t *testing.T) {
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"balance":999999,"holds":0,"available":999999,"account":"hanzo"}`))
	}))
	err := AuthorizeCompute(context.Background(), "acme", "", 100)
	if err == nil || !strings.Contains(err.Error(), "does not act for this org") {
		t.Fatalf("a balance read for another org must refuse, got %v", err)
	}
	// A member wallet inside the org is the org's own.
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing/balance" {
			_, _ = w.Write([]byte(`{"available":500,"account":"acme/alice"}`))
			return
		}
		_, _ = w.Write([]byte(`{"allow":true}`))
	}))
	if err := AuthorizeCompute(context.Background(), "acme", "", 100); err != nil {
		t.Fatalf("a wallet inside the org is the org's: %v", err)
	}
}

// A spend cap that refuses is the org's own policy, distinct from being broke;
// a cap verdict that cannot be read admits on funds alone.
func TestAuthorizeSeparatesTheCapFromFunds(t *testing.T) {
	for name, tc := range map[string]struct {
		verdict string
		status  int
		want    error
	}{
		"cap refuses":       {`{"allow":false,"reason":"spend_cap"}`, http.StatusOK, ErrSpendCapExceeded},
		"cap allows":        {`{"allow":true}`, http.StatusOK, nil},
		"cap read fails":    {`boom`, http.StatusInternalServerError, nil},
		"no cap configured": {``, http.StatusNotFound, nil},
	} {
		t.Run(name, func(t *testing.T) {
			commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/billing/balance" {
					commercetest.Balance(w, r, 10000)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.verdict))
			}))
			err := Authorize(context.Background(), "acme", "", "compute", 100)
			if !errors.Is(err, tc.want) && !(tc.want == nil && err == nil) {
				t.Fatalf("Authorize = %v, want %v", err, tc.want)
			}
		})
	}
}

// A refused identity is a refusal to authorize, never a pass: the gate is
// fail-closed on anything that is not a clear yes.
func TestAuthorizeFailsClosedWithoutAToken(t *testing.T) {
	srv := commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		commercetest.Balance(w, r, 10000)
	}))
	t.Setenv("iamEndpoint", srv.URL+"/nowhere")
	if err := AuthorizeCompute(context.Background(), "acme", "", 100); err == nil {
		t.Fatal("an identity IAM refuses must not authorize a launch")
	}
}

func TestDecimalIsExactCents(t *testing.T) {
	for cents, want := range map[int64]string{1: "0.01", 70: "0.70", 3178: "31.78", 100: "1.00", 12345678: "123456.78"} {
		if got := decimal(cents); got != want {
			t.Errorf("decimal(%d) = %q, want %q", cents, got, want)
		}
	}
}
