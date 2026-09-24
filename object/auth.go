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

package object

import (
	"slices"
	"strings"

	"github.com/hanzoai/compute/conf"
	"github.com/hanzoai/iamsdk/v2/iamsdk"
)

// GetBearerUser validates an "Authorization: Bearer <IAM JWT>" header and
// returns the authenticated user acting in org, or nil. org is the org the
// request names (X-Org-Id, or the owner it addresses); the user acts in it only
// when the token's signed membership includes it, in its home org when the
// request names none, and not at all when it names an org it is not a member of.
// iamsdk.ParseJwtToken verifies the token SIGNATURE (jwt.ParseWithClaims +
// x509), so a forged/tampered token is rejected.
//
// Signature alone is NOT sufficient, and three more things are checked:
//   - the ISSUER is this deployment's brand (iamIssuer): Hanzo IAM publishes ONE
//     shared JWKS holding every brand's cert, so a sibling brand's token
//     (lux.id/zoo.id/pars.id) verifies and is still not ours;
//   - the AUDIENCE is this service's own (iamAudience, else clientId): a token
//     minted for any other application of the brand is that application's, and
//     accepting it lets whoever holds one act here;
//   - the TENANT is the signed membership (`orgs`, home first), never `owner`.
//     IAM stamps `owner` with the MINTING APPLICATION's org, so a user of any
//     org signed in through an application filed under hanzo carries
//     owner=hanzo. A token with no membership fails closed rather than falling
//     back to that application's org.
//
// The returned user's Owner is the resolved tenant, so every reader downstream
// (resolveComputeOrg, Casbin's subOwner==objOwner) scopes to an org the user is
// a member of.
func GetBearerUser(authHeader, org string) *iamsdk.User {
	const prefix = "Bearer "
	if len(authHeader) <= len(prefix) || !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return nil
	}
	token := strings.TrimSpace(authHeader[len(prefix):])
	if token == "" {
		return nil
	}
	claims, err := iamsdk.ParseJwtToken(token)
	if err != nil || claims == nil {
		return nil
	}
	if !issuerAllowed(claims.RegisteredClaims.Issuer) {
		return nil // token from a different brand's IAM — reject on this surface
	}
	if !audienceAllowed(claims.RegisteredClaims.Audience) {
		return nil // minted for another application — not this service's to honour
	}
	tenant := member(claims.Orgs, org)
	if tenant == "" {
		return nil // no signed membership cannot be scoped — fail closed
	}
	user := claims.User
	user.Owner = tenant
	return &user
}

// member is the org a request acts in: the one it names, when the signed
// membership includes it; the home org (the first) when it names none. An org
// the membership does not include is refused — "" — rather than answered with
// another org the caller did not ask for. No membership is "".
func member(orgs []iamsdk.OrgRef, want string) string {
	want = strings.TrimSpace(want)
	if want == "" {
		if len(orgs) == 0 {
			return ""
		}
		return strings.TrimSpace(orgs[0].Org)
	}
	for _, o := range orgs {
		if strings.TrimSpace(o.Org) == want {
			return want
		}
	}
	return ""
}

// audienceAllowed reports whether a token was minted for this service: one of
// its audiences is in iamAudience (a single value or a comma list), or, with
// that unset, is this service's own clientId. Neither configured fails closed.
func audienceAllowed(audiences []string) bool {
	configured := strings.TrimSpace(conf.GetConfigString("iamAudience"))
	if configured == "" {
		configured = strings.TrimSpace(conf.GetConfigString("clientId"))
	}
	if configured == "" {
		return false
	}
	for want := range strings.SplitSeq(configured, ",") {
		want = strings.TrimSpace(want)
		if want != "" && slices.Contains(audiences, want) {
			return true
		}
	}
	return false
}

// issuerAllowed reports whether a token's issuer matches the configured brand
// issuer(s) in `iamIssuer`. iamIssuer may be a single value or a comma-separated
// list, and a trailing "/" is ignored on BOTH sides so "https://hanzo.id" and
// "https://hanzo.id/" are equivalent (IAM stamps the slashed form; the KMS mint
// and others the bare form — normalising here avoids that footgun). Empty config
// fails CLOSED: with no configured brand issuer we cannot bind a brand, so every
// bearer is rejected rather than silently admitting all brands.
func issuerAllowed(tokenIssuer string) bool {
	return matchIssuer(conf.GetConfigString("iamIssuer"), tokenIssuer)
}

// matchIssuer is the pure brand-issuer predicate (unit-tested). `configured` is
// the iamIssuer value (single or comma-list); `tokenIssuer` is the token's iss
// claim. Trailing slashes are ignored on both sides; empty `configured` or empty
// `tokenIssuer` fails closed.
func matchIssuer(configured, tokenIssuer string) bool {
	got := strings.TrimRight(strings.TrimSpace(tokenIssuer), "/")
	if got == "" {
		return false
	}
	raw := strings.TrimSpace(configured)
	if raw == "" {
		return false
	}
	for e := range strings.SplitSeq(raw, ",") {
		if want := strings.TrimRight(strings.TrimSpace(e), "/"); want != "" && strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}
