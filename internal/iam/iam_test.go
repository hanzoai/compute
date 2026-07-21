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

package iam

import (
	"encoding/json"
	"testing"
)

// TestIssuerEndpointDefault: with no config and no env, the endpoint falls back
// to the clean IAM default rather than an empty string.
func TestIssuerEndpointDefault(t *testing.T) {
	t.Setenv("IAM_ENDPOINT", "")
	t.Setenv("IAM_ISSUER", "")
	InitConfig("", "", "", "", "", "")
	if got := issuerEndpoint(); got != DefaultIssuer {
		t.Fatalf("issuerEndpoint() = %q, want %q", got, DefaultIssuer)
	}

	InitConfig("https://iam.hanzo.ai", "cid", "sec", "", "hanzo", "app-visor")
	if got := issuerEndpoint(); got != "https://iam.hanzo.ai" {
		t.Fatalf("issuerEndpoint() = %q, want configured endpoint", got)
	}
}

// TestIssuerEndpointEnvFallback: an unconfigured endpoint reads IAM_ENDPOINT.
func TestIssuerEndpointEnvFallback(t *testing.T) {
	InitConfig("", "", "", "", "", "")
	t.Setenv("IAM_ISSUER", "")
	t.Setenv("IAM_ENDPOINT", "https://lux.id")
	if got := issuerEndpoint(); got != "https://lux.id" {
		t.Fatalf("issuerEndpoint() = %q, want env IAM_ENDPOINT", got)
	}
}

// TestParseJwtTokenFailsClosed: garbage, empty, and structurally-broken tokens
// are all rejected — no token ever verifies without a real signature.
func TestParseJwtTokenFailsClosed(t *testing.T) {
	InitConfig("https://hanzo.id", "", "", "", "", "")
	for _, tok := range []string{"", "not-a-jwt", "a.b.c", "a.b"} {
		if claims, err := ParseJwtToken(tok); err == nil || claims != nil {
			t.Fatalf("ParseJwtToken(%q) = (%v, %v), want (nil, error)", tok, claims, err)
		}
	}
}

// TestClaimsDecodeFlat: the clean IAM mints identity fields flat at the JWT top
// level; the embedded User and RegisteredClaims both decode from one payload.
func TestClaimsDecodeFlat(t *testing.T) {
	payload := `{
		"owner": "acme",
		"name": "alice",
		"email": "alice@acme.example",
		"displayName": "Alice",
		"iss": "https://hanzo.id",
		"sub": "acme/alice"
	}`
	var c Claims
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	if c.Owner != "acme" || c.Name != "alice" || c.Email != "alice@acme.example" || c.DisplayName != "Alice" {
		t.Fatalf("user claims mismatch: %+v", c.User)
	}
	if c.RegisteredClaims.Issuer != "https://hanzo.id" || c.RegisteredClaims.Subject != "acme/alice" {
		t.Fatalf("registered claims mismatch: iss=%q sub=%q", c.RegisteredClaims.Issuer, c.RegisteredClaims.Subject)
	}
}

// TestUserGetIdAndMarshal: GetId is the IAM composite key, and the add-user body
// carries only the fields visor sets (empty ones omitted).
func TestUserGetIdAndMarshal(t *testing.T) {
	u := &User{Owner: "acme", Name: "bot-1", DisplayName: "bot-1", Type: "service-account", Tag: "agent"}
	if got := u.GetId(); got != "acme/bot-1" {
		t.Fatalf("GetId() = %q, want acme/bot-1", got)
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round["owner"] != "acme" || round["type"] != "service-account" || round["tag"] != "agent" {
		t.Fatalf("marshal shape mismatch: %s", b)
	}
	// isDeleted/isAdmin/isForbidden are omitempty and must not appear when false.
	if _, ok := round["isDeleted"]; ok {
		t.Fatalf("false isDeleted should be omitted: %s", b)
	}
}
