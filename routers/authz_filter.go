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
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/authz"
	"github.com/hanzoai/compute/util"
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

// pathTarget reads the (owner, name) an address names, from the address.
//
// This seam runs as middleware, and MIDDLEWARE HAS NO ROUTE PARAMETERS. Measured:
// a handler on /v1/assets/:owner/:name reads owner="acme" while the same request
// in middleware reads "". So a path-addressed resource authorized through
// c.Param decides on an EMPTY owner — it denies where the query spelling
// allowed, and no status assertion in the handler's own test can see it.
//
// The path is the one thing both halves agree on, and the grammar is total
// because every owned resource is addressed the same way:
//
//	/v1/{kind}                      -> "",    ""
//	/v1/{kind}/{owner}              -> owner, ""
//	/v1/{kind}/{owner}/{name}       -> owner, name
//	/v1/{kind}/{owner}/{name}/{sub} -> owner, name   (a sub-resource of the item)
//
// That uniformity is the point of addressing every owned thing the same way: a
// second shape would need a table here, and a table is a thing to keep in
// agreement with the router.
func pathTarget(path string) (string, string) {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	// seg[0] is the version, seg[1] the kind.
	if len(seg) < 3 || seg[0] != "v1" {
		return "", ""
	}
	if len(seg) == 3 {
		return seg[2], ""
	}
	return seg[2], seg[3]
}

func getObject(c *zip.Ctx) (string, string) {
	method := c.Method()

	// The address first: it is the most specific thing the caller said, and it
	// is what a resource surface puts the target in.
	if o, n := pathTarget(c.Path()); o != "" {
		return o, n
	}

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
