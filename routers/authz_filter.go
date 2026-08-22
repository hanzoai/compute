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

// addressedNouns are the /v1 nouns that name their object in the ADDRESS:
// /v1/<noun> is a collection scoped by ?owner, /v1/<noun>/<owner>/<name> is one
// item, and anything below that item belongs to it. One entry per family that
// has moved off the verb addresses.
var addressedNouns = map[string]bool{
	"records": true,
}

// addressedObject reads the object a moved noun's address names, and it is the
// ONE resolution for those addresses — the item from its two segments, the
// collection from the ?owner its handler lists by.
//
// Being one resolution is the point. The fallbacks below try `?id`, then
// `?owner`, then the body, and a request may carry more than one: `?owner=b&id=
// a/x` authorized against org a while the listing handler returned org b's rows.
// Naming the object once, from the same place the handler reads it, is what
// makes that impossible rather than merely unlikely.
//
// It reads the raw path rather than c.Param because this runs as MIDDLEWARE,
// where the matched route is the middleware's own and every route param answers
// "" — measured by TestRouteParamsAreInvisibleToMiddleware. An address whose
// owner segment the authorizer cannot see is an address with no tenant check:
// objOwner falls to "", the subOwner == objOwner clause cannot hold, and the
// request lands on the static policy, which names no such route.
//
// Keyed on the NOUN and not on segment count: /v1/k8s/clusters/<id> is four
// segments too and names no owner in the third, so a blanket rule would read a
// cluster id as an org.
func addressedObject(path, queryOwner string) (string, string, bool) {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) < 2 || seg[0] != "v1" || !addressedNouns[seg[1]] {
		return "", "", false
	}
	if len(seg) < 4 || seg[2] == "" || seg[3] == "" {
		return queryOwner, "", true
	}
	return seg[2], seg[3], true
}

func getObject(c *zip.Ctx) (string, string) {
	method := c.Method()

	// The ADDRESS is the first place a request names its object, and the best:
	// the handler resolves the same segments, so the authorizer judges what the
	// handler will act on. A query parameter or a body field can name a different
	// object than the one the URL does.
	if owner, name, ok := addressedObject(c.Path(), c.Query("owner")); ok {
		return owner, name
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
