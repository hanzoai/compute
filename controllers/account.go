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
	"context"
	_ "embed"
	"encoding/base64"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/conf"
	"github.com/hanzoai/visor/object"
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

// Signin exchanges an IAM authorization code for that IAM's token and hands the
// decoded claims back to the browser that started the round trip.
//
// Both failures are the CALLER's input — a code that IAM refuses, and a token
// that does not verify — so both are answered, not panicked. They used to panic:
// the recover filter turns that into a 500 carrying zip's error shape, which the
// page driving this flow reads as a success with no message, so a refused login
// rendered as a blank error.
func (c *ApiController) Signin() {
	code := c.Ctx.Query("code")
	state := c.Ctx.Query("state")

	token, err := iamsdk.GetOAuthToken(code, state)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	claims, err := iamsdk.ParseJwtToken(token.AccessToken)
	if err != nil {
		c.ResponseError(err.Error())
		return
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

// Credential is the whole input of a read whose subject IS the caller's own
// credential: the forwarded IAM Bearer, and nothing else to address.
//
// It is deliberately not Scope. Scope carries the ?owner a SERVICE subject scopes
// a list with, and an account is never another org's to ask for — the answer is
// whoever the presented token names, or nobody. Publishing an owner here would
// document a parameter this op must ignore, and "who am I" would read like a user
// lookup, which belongs to IAM and not to a compute service.
type Credential struct {
	Authorization string `json:"-" header:"Authorization"`
}

// Account is the identity a credential names — what the caller is, to visor.
//
// It carries the identity and NOT the credential. The address it replaced
// answered with the whole decoded claim set, so it handed the caller back the
// access token it had just sent, beside the empty password fields iamsdk.User
// declares. Neither is anything a caller learns by asking, and a body is copied
// further than the request that produced it.
type Account struct {
	// Owner is the org the account belongs to — the tenant every other op on this
	// service scopes to.
	Owner string `json:"owner"`
	// Name is the account's name within that org. Owner/Name is its id.
	Name string `json:"name"`
	// DisplayName is how the account asks to be shown, when it has said.
	DisplayName string `json:"displayName,omitempty"`
	// Email is the account's address, when the token carries one.
	Email string `json:"email,omitempty"`
	// Avatar is a URL to the account's picture, when it has one.
	Avatar string `json:"avatar,omitempty"`
	// IsAdmin reports whether the account administers its own org. It is NOT
	// platform authority, which is membership of the reserved admin org.
	IsAdmin bool `json:"isAdmin"`
}

// GetAccount answers which identity the presented credential names.
//
// It reads the forwarded IAM Bearer and nothing else: visor mints no identity and
// keeps no session, so this is a decode of what the caller already holds — which
// is exactly why it is a resource rather than a verb. An unauthenticated caller
// is told so by the STATUS: the address used to answer 200 carrying
// {"status":"error","msg":"please sign in first"}, and a caller that has to read
// a field of a success to learn it failed is one that will forget to.
//
// Response: {"owner": "acme", "name": "alice", "displayName": "Alice", "isAdmin": false}
func GetAccount(_ context.Context, in *Credential) (*Account, error) {
	user := object.GetBearerUser(in.Authorization)
	if user == nil {
		return nil, zip.ErrUnauthorized("no credential names an account")
	}
	return &Account{
		Owner:       user.Owner,
		Name:        user.Name,
		DisplayName: user.DisplayName,
		Email:       user.Email,
		Avatar:      user.Avatar,
		IsAdmin:     user.IsAdmin,
	}, nil
}
