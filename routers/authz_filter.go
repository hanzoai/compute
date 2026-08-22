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
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/authz"
	"github.com/hanzoai/visor/util"
)

type Object struct {
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	AccessKey    string `json:"accessKey"`
	AccessSecret string `json:"accessSecret"`
}

func getSubject(c *zip.Ctx) (string, string) {
	username := getUsername(c)
	if username == "" {
		return "anonymous", "anonymous"
	}

	// username == "built-in/admin"
	return util.GetOwnerAndNameFromId(username)
}

// getObject resolves WHICH object a request acts on, which is half of the
// authorization decision (authz.IsAllowed admits a subject to its OWN org's
// objects — subOwner == objOwner).
//
// An address that CARRIES the object names it in the ROUTE:
// /v1/providers/:owner/:name is the same (owner, name) pair the retired
// ?id=owner/name carried, moved out of the query string. Reading it here is what
// keeps the decision identical across that move — the seam resolves the object
// from wherever the address puts it, and the rule it feeds is untouched.
//
// Only those two names are read, and deliberately not `:id`: an `:id` elsewhere
// on this surface is a cloud's own machine or cluster identifier, which is not
// an org and must not be compared to one.
func getObject(c *zip.Ctx) (string, string) {
	if owner := c.Fiber().Params("owner"); owner != "" {
		return owner, c.Fiber().Params("name")
	}

	method := c.Method()

	if method == http.MethodGet {
		// query == "?id=built-in/admin"
		id := c.Query("id")
		if id != "" {
			return util.GetOwnerAndNameFromIdNoCheck(id)
		}

		owner := c.Query("owner")
		if owner != "" {
			return owner, ""
		}

		return "", ""
	} else {
		id := c.Query("id")
		if id != "" {
			return util.GetOwnerAndNameFromIdNoCheck(id)
		}

		body := c.Body()
		if len(body) == 0 {
			id := c.Fiber().FormValue("id")
			if id != "" {
				return util.GetOwnerAndNameFromIdNoCheck(id)
			}

			return c.Fiber().FormValue("owner"), c.Fiber().FormValue("name")
		}

		var obj Object
		err := json.Unmarshal(body, &obj)
		if err != nil {
			return "", ""
		}

		return obj.Owner, obj.Name
	}
}

func willLog(subOwner string, subName string, method string, urlPath string, objOwner string, objName string) bool {
	if subOwner == "anonymous" && subName == "anonymous" && method == "GET" && (urlPath == "/v1/get-account") && objOwner == "" && objName == "" {
		return false
	}
	return true
}

func getUrlPath(urlPath string) string {
	return urlPath
}

// ApiFilter is the ONE authorization seam — ZAP middleware that authorizes every
// request against the static Casbin policy (authz.IsAllowed) on the resolved
// subject/object, denying with 403 or threading the request through to the
// handler via c.Next(). It runs after the tenant filter and before the record
// filter, exactly as the Beego BeforeRouter chain did.
func ApiFilter(c *zip.Ctx) error {
	subOwner, subName := getSubject(c)
	method := c.Method()
	urlPath := getUrlPath(c.Path())
	objOwner, objName := getObject(c)

	user := GetSessionUser(c)
	isAllowed := authz.IsAllowed(user, subOwner, subName, method, urlPath, objOwner, objName)

	result := "deny"
	if isAllowed {
		result = "allow"
	}

	if willLog(subOwner, subName, method, urlPath, objOwner, objName) {
		logLine := fmt.Sprintf("subOwner = %s, subName = %s, method = %s, urlPath = %s, obj.Owner = %s, obj.Name = %s, result = %s",
			subOwner, subName, method, urlPath, objOwner, objName, result)
		fmt.Println(logLine)
		util.LogInfo(c, logLine)
	}

	if !isAllowed {
		return requestDeny(c)
	}

	return c.Next()
}
