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

package controllers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/iamsdk/v2/iamsdk"

	"github.com/hanzoai/compute/object"
)

// principal is the ONE org resolution both visor surfaces run — the controller methods
// through resolveComputeOrg, the typed ops through their decoded input — so these
// cases pin the rule once for both.
//
// What is covered here is the ?owner leg and the fail-closed floor. The other leg
// (a real user's bearer WINS over ?owner) is GetBearerUser returning non-nil, and
// reaching it needs a signed IAM token bound to a configured brand issuer; it is
// not restated here, because there is no second copy of the rule to drift — both
// surfaces call this function, and a bearer that establishes a user is the only
// way past the first branch.
func TestPrincipalOrg(t *testing.T) {
	cases := []struct {
		name          string
		authorization string
		owner         string
		want          string
	}{
		{"service call names its org", "", "acme", "acme"},
		{"owner is trimmed", "", "  acme  ", "acme"},
		{"no credential and no owner fails closed", "", "", ""},
		{"whitespace-only owner fails closed", "", "   ", ""},
		{"unparseable bearer establishes nothing", "Bearer not-a-token", "acme", "acme"},
		{"non-bearer scheme establishes nothing", "Basic dXNlcjpwYXNz", "acme", "acme"},
		{"empty bearer establishes nothing", "Bearer ", "acme", "acme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, got := principal(c.authorization, c.owner); got != c.want {
				t.Fatalf("principal(%q, %q) org = %q, want %q", c.authorization, c.owner, got, c.want)
			}
		})
	}
}

// visorAudience is the audience a token minted for this service carries.
const visorAudience = "hanzo-visor"

// signer mints tokens the way IAM does, so a test can present a bearer that
// GetBearerUser actually accepts: RS256 over a key whose self-signed certificate
// is the SDK's configured PEM, with an issuer this deployment is bound to, this
// service's audience, and IAM's real claim shape — `owner` is the MINTING
// APPLICATION's org (hanzo, for every app filed there), and the user's own org
// is the signed membership `orgs`.
//
// Without it the precedence rule below is untestable, and an untestable rule is
// one a refactor can invert silently — which is the whole reason it is here.
func signer(t *testing.T, issuer string) func(org string) string {
	t.Helper()
	sign := signing(t, issuer)
	return func(org string) string {
		return sign(func(c *iamsdk.Claims) { c.Orgs = []iamsdk.OrgRef{{Org: org, Role: "member"}} })
	}
}

// signing is signer with every claim open to the test: edit starts from a token
// minted through an application filed under hanzo, for this service, with no
// membership.
func signing(t *testing.T, issuer string) func(edit func(*iamsdk.Claims)) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "visor-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	// Endpoint "" keeps ParseJwtToken on the PEM rather than reaching for JWKS.
	iamsdk.InitConfig("", "", "", cert, "", "")
	t.Cleanup(func() { iamsdk.InitConfig("", "", "", "", "", "") })
	t.Setenv("iamIssuer", issuer)
	t.Setenv("iamAudience", visorAudience)

	return func(edit func(*iamsdk.Claims)) string {
		claims := &iamsdk.Claims{}
		claims.Owner, claims.Name = "hanzo", "alice"
		claims.Issuer = issuer
		claims.Audience = []string{visorAudience}
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		if edit != nil {
			edit(claims)
		}
		s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return "Bearer " + s
	}
}

// TestOrgBearerBeatsOwner is the tenant boundary, stated as an assertion.
//
// A real user's org comes from a SIGNED claim; ?owner is a string the caller
// typed. If the query could win, any customer holding any valid token could read
// any other tenant's fleet by appending ?owner=<them> — so the precedence is not
// a preference, it is the isolation. Both visor surfaces resolve through this one
// function, so this pins it for the typed op and the controller methods at once.
func TestPrincipalBearerBeatsOwner(t *testing.T) {
	mint := signer(t, "https://test.id")

	if _, got := principal(mint("trueorg"), "victim"); got != "trueorg" {
		t.Fatalf("principal(bearer(trueorg), ?owner=victim) org = %q, want %q — a signed claim must beat a typed query", got, "trueorg")
	}
	if _, got := principal(mint("trueorg"), ""); got != "trueorg" {
		t.Fatalf("principal(bearer(trueorg), no owner) org = %q, want %q", got, "trueorg")
	}
	// A token from another brand's IAM verifies its signature and is still not
	// this deployment's user, so it establishes nothing — and must NOT then fall
	// through to a ?owner the caller chose.
	other := signer(t, "https://other.id")
	t.Setenv("iamIssuer", "https://test.id")
	if _, got := principal(other("trueorg"), "victim"); got != "victim" {
		t.Fatalf("principal(foreign bearer, ?owner=victim) org = %q, want %q — a rejected token leaves the service-call branch", got, "victim")
	}
}

// The tenant is the user's signed membership, never `owner`. IAM stamps `owner`
// with the minting application's org, so a user of acme signed in through any
// application filed under hanzo carries owner=hanzo; read as the tenant, that
// would let them act on org hanzo's machines. They act in hanzo only if they are
// a member of it, and in their own org otherwise.
func TestAUserActsOnlyInAnOrgTheyAreAMemberOf(t *testing.T) {
	sign := signing(t, "https://test.id")
	acmeOnly := sign(func(c *iamsdk.Claims) { c.Orgs = []iamsdk.OrgRef{{Org: "acme", Role: "owner"}} })

	for _, asked := range []string{"hanzo", "", "victim"} {
		if _, got := principal(acmeOnly, asked); got != "acme" {
			t.Errorf("acme's user signed in through a hanzo app, asking for %q, acts in %q — want acme", asked, got)
		}
	}
	both := sign(func(c *iamsdk.Claims) {
		c.Orgs = []iamsdk.OrgRef{{Org: "acme", Role: "owner"}, {Org: "hanzo", Role: "member"}}
	})
	if _, got := principal(both, "hanzo"); got != "hanzo" {
		t.Errorf("a member of hanzo asking for hanzo acts in %q", got)
	}
	if _, got := principal(both, ""); got != "acme" {
		t.Errorf("a member of two orgs asking for none acts in %q, want the home org acme", got)
	}

	// No membership is no tenant: never the application's org.
	if u := object.GetBearerUser(sign(nil), "hanzo"); u != nil {
		t.Fatalf("a token with no membership acts in %q", u.Owner)
	}
	// A token minted for another application is not this service's to honour.
	other := sign(func(c *iamsdk.Claims) {
		c.Audience = []string{"hanzo-chat"}
		c.Orgs = []iamsdk.OrgRef{{Org: "hanzo"}}
	})
	if u := object.GetBearerUser(other, "hanzo"); u != nil {
		t.Fatalf("a token for hanzo-chat was accepted here, acting in %q", u.Owner)
	}
	// And with no audience configured at all, nothing is.
	t.Setenv("iamAudience", "")
	t.Setenv("clientId", "")
	if u := object.GetBearerUser(both, "hanzo"); u != nil {
		t.Fatal("a bearer was accepted with no audience configured")
	}
	t.Setenv("clientId", visorAudience)
	if u := object.GetBearerUser(both, "hanzo"); u == nil {
		t.Fatal("clientId is this service's audience when iamAudience is unset")
	}
}
