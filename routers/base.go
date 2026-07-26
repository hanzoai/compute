// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package routers

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/conf"
	iam "github.com/hanzoai/visor/internal/iam"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

type Response struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   interface{} `json:"data"`
	Data2  interface{} `json:"data2"`
}

// GetSessionUser resolves the caller from the forwarded IAM Bearer JWT — the ONE
// stateless identity seam. There is no cookie/redis session: an API/console
// caller presents a short-lived Bearer, verified and brand-bound in
// object.GetBearerUser.
func GetSessionUser(c *zip.Ctx) *iam.User {
	return object.GetBearerUser(c.Header("Authorization"))
}

func getUsername(c *zip.Ctx) (username string) {
	user := GetSessionUser(c)
	if user != nil {
		username = util.GetIdFromOwnerAndName(user.Owner, user.Name)
	} else {
		username, _ = getUsernameByClientIdSecret(c)
	}
	return
}

func requestDeny(c *zip.Ctx) error {
	response := &Response{
		Status: "error",
		Msg:    "Unauthorized operation",
	}
	return c.JSON(403, response)
}

// basicAuth parses an "Authorization: Basic <base64(id:secret)>" header, RFC
// 7617 — splitting on the FIRST colon so a secret may contain one. The ZAP
// replacement for net/http's Request.BasicAuth.
func basicAuth(c *zip.Ctx) (id, secret string, ok bool) {
	const p = "Basic "
	h := c.Header("Authorization")
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(p):]))
	if err != nil {
		return "", "", false
	}
	id, secret, found := strings.Cut(string(raw), ":")
	if !found {
		return "", "", false
	}
	return id, secret, true
}

func getUsernameByClientIdSecret(c *zip.Ctx) (string, error) {
	clientId, clientSecret, ok := basicAuth(c)
	if !ok {
		clientId = c.Query("clientId")
		clientSecret = c.Query("clientSecret")
	}

	if clientId == "" || clientSecret == "" {
		return "", nil
	}

	applicationName := conf.GetConfigString("iamApplication")
	if clientSecret != conf.GetConfigString("clientSecret") {
		return "", fmt.Errorf("Incorrect client secret for application: %s", applicationName)
	}

	return fmt.Sprintf("app/%s", applicationName), nil
}
