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

// Package commercetest fakes the commerce API cloud serves, behind the IAM token
// endpoint visor mints its identity at, for tests of visor's one commerce client
// (service/commerce.go).
package commercetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ClientID and Token are the identity the fake issues: visor presents
// "Bearer "+Token on every commerce call.
const (
	ClientID = "hanzo-visor"
	Token    = "visor-iam-token"
)

// Serve starts the fake and points visor at it: the commerce base and the IAM
// endpoint both name it, and visor's client credential is set. The token mint is
// answered here; every other request reaches h.
func Serve(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/iam/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, Token)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMMERCE_URL", srv.URL)
	t.Setenv("iamEndpoint", srv.URL)
	t.Setenv("clientId", ClientID)
	t.Setenv("clientSecret", "visor-secret")
	return srv
}

// Usage is one POST /v1/billing/usage body.
type Usage struct {
	ID     string `json:"id"`
	Org    string `json:"org"`
	Amount struct {
		Decimal  string `json:"decimal"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Service string `json:"service"`
	Model   string `json:"model"`
	Project string `json:"project"`
}

// Read decodes the usage body of r. A body that is not one reads as zero.
func Read(r *http.Request) Usage {
	var u Usage
	_ = json.NewDecoder(r.Body).Decode(&u)
	return u
}

// Cents is the amount in whole cents; an amount finer than a cent reads as -1.
func (u Usage) Cents() int64 {
	whole, frac, _ := strings.Cut(u.Amount.Decimal, ".")
	if len(frac) > 2 {
		return -1
	}
	frac = (frac + "00")[:2]
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil {
		return -1
	}
	return w*100 + f
}

// Balance answers a balance read of cents, for the org the request names.
func Balance(w http.ResponseWriter, r *http.Request, cents int64) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"balance": cents, "holds": 0, "available": cents, "account": r.Header.Get("X-Org-Id"),
	})
}
