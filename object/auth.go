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
	"strings"

	iam "github.com/hanzoai/iam"
	"github.com/hanzoai/visor/conf"
)

// GetBearerUser validates an "Authorization: Bearer <IAM JWT>" header and
// returns the authenticated user, or nil. iam.ParseJwtToken verifies the token
// SIGNATURE (jwt.ParseWithClaims + x509), so a forged/tampered token is rejected.
//
// Signature alone is NOT sufficient: Hanzo IAM publishes ONE shared JWKS holding
// every brand's cert (hanzo/lux/zoo/pars/...), so a validly-signed token from any
// brand — including public self-service signups — passes the signature check. We
// therefore bind the token to THIS deployment by BRAND (issuer), not by a single
// org:
//   - owner (org) must be non-empty (an empty-org token can't be scoped), and
//   - issuer must match the configured brand issuer(s) in iamIssuer, so a sibling
//     brand's token (lux.id/zoo.id/pars.id) is rejected even though its signature
//     verifies.
// EVERY org within this brand is accepted — the resell compute surface is
// multi-tenant (org "hanzo", "maxpower", and every self-service customer org).
// Each request is org-scoped downstream: resolveComputeOrg pins org = user.Owner
// (no ?owner override for real users) and Casbin enforces subOwner==objOwner, so
// one org can never reach another's machines. This is how API/console callers
// authenticate as a user (org = user.Owner) from a forwarded short-lived Bearer,
// without a browser cookie session.
func GetBearerUser(authHeader string) *iam.User {
	const prefix = "Bearer "
	if len(authHeader) <= len(prefix) || !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return nil
	}
	token := strings.TrimSpace(authHeader[len(prefix):])
	if token == "" {
		return nil
	}
	claims, err := iam.ParseJwtToken(token)
	if err != nil || claims == nil {
		return nil
	}
	owner := strings.TrimSpace(claims.User.Owner)
	if owner == "" {
		return nil // empty-org token cannot be scoped — fail closed
	}
	if !issuerAllowed(claims.RegisteredClaims.Issuer) {
		return nil // token from a different brand's IAM — reject on this surface
	}
	return &claims.User
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
	for _, e := range strings.Split(raw, ",") {
		if want := strings.TrimRight(strings.TrimSpace(e), "/"); want != "" && strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}
