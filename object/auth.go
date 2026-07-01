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
)

// GetBearerUser validates an "Authorization: Bearer <IAM JWT>" header and
// returns the authenticated user, or nil. iam.ParseJwtToken verifies the token
// SIGNATURE against the application certificate (jwt.ParseWithClaims + x509), so
// a forged or tampered token is rejected. This is how API and console callers
// authenticate as a user — the caller's org is user.Owner — without a browser
// cookie session, enabling per-org scoping from a forwarded short-lived Bearer.
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
	return &claims.User
}
