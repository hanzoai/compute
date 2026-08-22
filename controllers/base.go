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
	"github.com/hanzoai/visor/util"
	"strings"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// ApiController is the ZAP-native controller base. It carries the request
// context (zip.Ctx) and the buffered JSON response body (Data["json"]) the
// record filter reads back. It replaces the Beego controller one-for-one:
// handlers keep their method receiver, but the framework underneath is zip over
// fiber/fasthttp — no cookie or server-side session. A caller authenticates statelessly
// with a forwarded IAM Bearer JWT (object.GetBearerUser); the resolved Principal
// is the ONE identity seam.
type ApiController struct {
	Ctx  *zip.Ctx
	Data map[string]interface{}
}

// New builds a controller bound to a request context, with its response buffer
// ready. This is the ONE construction path the route wrappers use.
func New(c *zip.Ctx) *ApiController {
	return &ApiController{Ctx: c, Data: map[string]interface{}{}}
}

func GetUserName(user *iamsdk.User) string {
	if user == nil {
		return ""
	}

	return user.Name
}

func wrapActionResponse(affected bool, e ...error) *Response {
	if len(e) != 0 && e[0] != nil {
		return &Response{Status: "error", Msg: e[0].Error()}
	} else if affected {
		return &Response{Status: "ok", Msg: "", Data: "Affected"}
	} else {
		return &Response{Status: "ok", Msg: "", Data: "Unaffected"}
	}
}

// GetSessionClaims parses the forwarded IAM Bearer JWT into its claims — the
// stateless replacement for the Beego cookie session. Signature is verified by
// iamsdk.ParseJwtToken; brand/issuer binding is enforced by the ApiFilter (via
// object.GetBearerUser) before any handler runs, so this is a plain decode.
func (c *ApiController) GetSessionClaims() *iamsdk.Claims {
	const prefix = "Bearer "
	h := c.Ctx.Header("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return nil
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return nil
	}
	claims, err := iamsdk.ParseJwtToken(token)
	if err != nil || claims == nil {
		return nil
	}
	return claims
}

// SetSessionClaims is a no-op in the stateless model: there is no server-side
// session to write. Signin returns the claims (and access token) to the client,
// which then carries the token as a Bearer on every subsequent request.
func (c *ApiController) SetSessionClaims(claims *iamsdk.Claims) {}

// GetSessionUser resolves the authenticated user from the forwarded IAM Bearer
// JWT (brand/issuer-bound in object.GetBearerUser), or nil.
func (c *ApiController) GetSessionUser() *iamsdk.User {
	return object.GetBearerUser(c.Ctx.Header("Authorization"))
}

// SetSessionUser is a no-op in the stateless model (see SetSessionClaims).
func (c *ApiController) SetSessionUser(user *iamsdk.User) {}

func (c *ApiController) GetSessionUsername() string {
	user := c.GetSessionUser()
	if user == nil {
		return ""
	}

	return GetUserName(user)
}

// Id is the identifier this request addresses, in the spelling object.* takes.
//
// It reads the ADDRESS first and the query only when the address does not carry
// it. A resource surface puts the target in the path; the surface this replaces
// put it in ?id=. One reader means a handler cannot answer for the right thing
// at one spelling and the wrong thing at the other — and reading only the query
// at a path address yields the EMPTY id, which is a different object, not an
// error.
//
// The query value is returned VERBATIM. Not every id is owner/name — a node
// pool is addressed by bare name — so parsing here and rebuilding would turn an
// id this service accepts into one it does not.
func (c *ApiController) Id() string {
	if o := c.Ctx.Param("owner"); o != "" {
		if n := c.Ctx.Param("name"); n != "" {
			return o + "/" + n
		}
		return o
	}
	return c.Ctx.Query("id")
}

// Target is Id split into its parts, for the callers that want them apart.
func (c *ApiController) Target() (string, string) {
	if id := c.Id(); id != "" {
		return util.GetOwnerAndNameFromIdNoCheck(id)
	}
	return c.Ctx.Query("owner"), c.Ctx.Query("name")
}
