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
	"github.com/hanzoai/vm/conf"
)

// GetBearerUser validates an "Authorization: Bearer <IAM JWT>" header and
// returns the authenticated user, or nil. iam.ParseJwtToken verifies the token
// SIGNATURE (jwt.ParseWithClaims + x509), so a forged/tampered token is rejected.
//
// Signature alone is NOT sufficient: Hanzo IAM publishes ONE shared JWKS holding
// every brand's cert (hanzo/lux/zoo/pars/...), so a validly-signed token from any
// brand — including public self-service signups — passes the signature check. We
// therefore bind the token to THIS deployment:
//   - owner (org) must be non-empty (an empty-org token can't be scoped), and
//   - owner must equal the configured org (casdoorOrganization) so a sibling
//     brand's token is rejected even though its signature verifies, and
//   - issuer must equal the configured issuer when one is set (iamIssuer).
// This is how API/console callers authenticate as a user (org = user.Owner) from
// a forwarded short-lived Bearer, without a browser cookie session.
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
	if org := conf.GetConfigString("casdoorOrganization"); org != "" && !strings.EqualFold(owner, org) {
		return nil // cross-brand / cross-org token — reject on this surface
	}
	if iss := conf.GetConfigString("iamIssuer"); iss != "" && claims.RegisteredClaims.Issuer != iss {
		return nil
	}
	return &claims.User
}
