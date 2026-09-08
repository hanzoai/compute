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

	"github.com/hanzoai/iamsdk/v2/iamsdk"

	"github.com/hanzoai/compute/conf"
)

//go:embed token_jwt_key.pem
var JwtPublicKey string

func init() {
	InitAuthConfig()
}

func InitAuthConfig() {
	iamEndpoint := conf.GetConfigString("iamEndpoint")
	clientId := conf.GetConfigString("clientId")
	clientSecret := conf.GetConfigString("clientSecret")
	iamOrganization := conf.GetConfigString("iamOrganization")
	iamApplication := conf.GetConfigString("iamApplication")

	// The JWT verification cert is config-driven (KMS-synced) so a deployment
	// verifies its own org's tokens (e.g. cert-hanzo) rather than the embedded
	// upstream default. Delivered base64 to stay single-line-safe in the
	// app.conf ini; falls back to the embedded PEM when unset.
	jwtPublicKey := conf.GetConfigString("jwtPublicKey")
	if b64 := conf.GetConfigString("jwtPublicKeyBase64"); b64 != "" {
		if dec, err := base64.StdEncoding.DecodeString(b64); err == nil {
			jwtPublicKey = string(dec)
		}
	}
	if jwtPublicKey == "" {
		jwtPublicKey = JwtPublicKey
	}

	iamsdk.InitConfig(iamEndpoint, clientId, clientSecret, jwtPublicKey, iamOrganization, iamApplication)
}

func (c *ApiController) Signin() {
	code := c.Ctx.Query("code")
	state := c.Ctx.Query("state")

	token, err := iamsdk.GetOAuthToken(code, state)
	if err != nil {
		panic(err)
	}

	claims, err := iamsdk.ParseJwtToken(token.AccessToken)
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
