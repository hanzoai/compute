// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEgressIdentityIsMintedFromIAM(t *testing.T) {
	var calls int
	var gotUser, gotPass, gotGrant, gotResource, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		_ = r.ParseForm()
		gotGrant, gotResource = r.Form.Get("grant_type"), r.Form.Get("resource")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, calls)
	}))
	defer srv.Close()

	who := &identity{
		endpoint: srv.URL, id: "visor", secret: "shh",
		audience: "hanzo-egress", client: srv.Client(),
	}

	tok, err := who.token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok != "tok-1" {
		t.Errorf("token = %q, want tok-1", tok)
	}

	// The exchange is HIP-0111's: client_secret_basic at the IAM token address,
	// scoped to egress by RFC 8707 `resource`.
	if gotPath != "/v1/iam/oauth/token" {
		t.Errorf("minted at %q, want /v1/iam/oauth/token", gotPath)
	}
	if gotUser != "visor" || gotPass != "shh" {
		t.Errorf("basic auth = %q/%q, want visor/shh — the identity must be the client credential", gotUser, gotPass)
	}
	if gotGrant != "client_credentials" {
		t.Errorf("grant_type = %q, want client_credentials", gotGrant)
	}
	if gotResource != "hanzo-egress" {
		t.Errorf("resource = %q, want hanzo-egress — an unscoped token spends anywhere", gotResource)
	}

	// Held while it is live: a mint per cloud call would put IAM in the path of
	// every request egress already authenticates.
	if _, err := who.token(); err != nil || calls != 1 {
		t.Errorf("second call minted again (calls=%d): a live token must be reused", calls)
	}

	// Replaced BEFORE it expires, so a token never dies in flight.
	who.until = time.Now().Add(early / 2)
	if tok, err := who.token(); err != nil || tok != "tok-2" {
		t.Errorf("near expiry token = %q (calls=%d), want a fresh tok-2", tok, calls)
	}
}

// A refusal names the identity that was refused. Without that, a 401 reads the
// same whether the id is wrong, the secret is stale, or the app may not use this
// grant — and the reader holds none of those.
func TestEgressIdentityRefusalNamesTheClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_client","error_description":"client authentication failed"}`)
	}))
	defer srv.Close()

	who := &identity{endpoint: srv.URL, id: "visor", secret: "wrong", client: srv.Client()}
	_, err := who.token()
	if err == nil {
		t.Fatal("a refused exchange must not yield a token")
	}
	for _, want := range []string{"visor", "401", "invalid_client"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}
