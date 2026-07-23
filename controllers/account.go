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

package controllers

import (
	_ "embed"
	"encoding/base64"

	iam "github.com/hanzoai/iam-v1"

	"github.com/hanzoai/visor/conf"
)

//go:embed token_jwt_key.pem
var JwtPublicKey string

func init() {
	InitAuthConfig()
}

// cfg returns the first non-empty app.conf value among keys — lets the rebrand
// read either the canonical iam* keys or the legacy casdoor* keys.
func cfg(keys ...string) string {
	for _, k := range keys {
		if v := conf.GetConfigString(k); v != "" {
			return v
		}
	}
	return ""
}

func InitAuthConfig() {
	iamEndpoint := cfg("iamEndpoint", "casdoorEndpoint")
	clientId := cfg("clientId")
	clientSecret := cfg("clientSecret")
	iamOrganization := cfg("iamOrganization", "casdoorOrganization")
	iamApplication := cfg("iamApplication", "casdoorApplication")

	// The JWT verification cert is config-driven (KMS-synced) so a deployment
	// verifies its own org's tokens (e.g. cert-hanzo) rather than the embedded
	// upstream default. Delivered base64 to stay single-line-safe in the
	// app.conf ini; falls back to the embedded PEM when unset.
	jwtPublicKey := cfg("jwtPublicKey")
	if b64 := cfg("jwtPublicKeyBase64"); b64 != "" {
		if dec, err := base64.StdEncoding.DecodeString(b64); err == nil {
			jwtPublicKey = string(dec)
		}
	}
	if jwtPublicKey == "" {
		jwtPublicKey = JwtPublicKey
	}

	iam.InitConfig(iamEndpoint, clientId, clientSecret, jwtPublicKey, iamOrganization, iamApplication)
}

func (c *ApiController) Signin() {
	code := c.Ctx.Query("code")
	state := c.Ctx.Query("state")

	token, err := iam.GetOAuthToken(code, state)
	if err != nil {
		panic(err)
	}

	claims, err := iam.ParseJwtToken(token.AccessToken)
	if err != nil {
		panic(err)
	}

	claims.AccessToken = token.AccessToken
	c.SetSessionClaims(claims)
	userId := claims.User.Owner + "/" + claims.User.Name
	c.Ctx.Locals("recordUserId", userId)

	c.ResponseOk(claims)
}

func (c *ApiController) Signout() {
	c.SetSessionClaims(nil)

	c.ResponseOk()
}

func (c *ApiController) GetAccount() {
	if c.RequireSignedIn() {
		return
	}

	claims := c.GetSessionClaims()

	c.ResponseOk(claims)
}
