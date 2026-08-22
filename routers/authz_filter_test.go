// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// TestRouteParamsAreInvisibleToMiddleware is the measurement addressedObject
// exists for, and it is worth a test because the obvious code is the wrong code:
// the authorizer runs as middleware, where the matched route is the middleware's
// own and c.Param answers "" for every name the real route declares.
//
// Written as an assertion rather than a comment because if fiber ever binds
// params before the chain, the path parsing can go and getObject can just ask.
func TestRouteParamsAreInvisibleToMiddleware(t *testing.T) {
	app := zip.New(zip.Config{})
	var seen string
	app.Use(zip.H(func(c *zip.Ctx) error {
		seen = c.Param("owner") + "/" + c.Param("name")
		return c.Next()
	}))
	app.Get("/v1/records/:owner/:name", func(c *zip.Ctx) error { return c.String(200, "ok") })

	if _, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/records/acme/abc", nil)); err != nil {
		t.Fatal(err)
	}
	if seen != "/" {
		t.Fatalf("middleware read route params as %q — itemAddress is no longer needed", seen)
	}
}

// TestAddressedObjectNamesTheObject pins what the authorizer reads off a moved
// noun's address, including the case a blanket segment-count rule gets wrong.
func TestAddressedObjectNamesTheObject(t *testing.T) {
	for _, tc := range []struct {
		path, queryOwner string
		owner, name      string
		ok               bool
	}{
		{path: "/v1/records/acme/abc", owner: "acme", name: "abc", ok: true},
		{path: "/v1/records/acme/abc/block", owner: "acme", name: "abc", ok: true},
		// The collection is the org its handler lists, and only that. A caller
		// that also sends ?id=mine/x gets authorized against acme, because acme
		// is whose rows come back.
		{path: "/v1/records", queryOwner: "acme", owner: "acme", ok: true},
		{path: "/v1/records", ok: true},
		// Four segments, and the third is a cluster id rather than an org. A rule
		// keyed on shape instead of on the noun would authorize this request
		// against an org named "clusters".
		{path: "/v1/k8s/clusters/xyz", ok: false},
		{path: "/v1/machines/drop-a/agent", ok: false},
	} {
		owner, name, ok := addressedObject(tc.path, tc.queryOwner)
		if ok != tc.ok || owner != tc.owner || name != tc.name {
			t.Errorf("addressedObject(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, tc.queryOwner, owner, name, ok, tc.owner, tc.name, tc.ok)
		}
	}
}

// TestRecordListIsAuthorizedAgainstTheOrgItLists is the second disagreement the
// address rule closes. getObject's fallbacks read `?id` before `?owner`, while
// the list handler reads `?owner` — so `?owner=victim&id=mine/x` was authorized
// against `mine` and answered with `victim`'s audit trail. The reachable request
// is the one written here.
func TestRecordListIsAuthorizedAgainstTheOrgItLists(t *testing.T) {
	app := zip.New(zip.Config{})
	var owner, name string
	app.Use(zip.H(func(c *zip.Ctx) error {
		owner, name = getObject(c)
		return c.Next()
	}))
	app.Get("/v1/records", func(c *zip.Ctx) error { return c.String(200, "ok") })

	if _, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/records?owner=victim&id=mine/x", nil)); err != nil {
		t.Fatal(err)
	}
	if owner != "victim" || name != "" {
		t.Fatalf("getObject = (%q, %q), want (victim, \"\") — the listing returns victim's rows", owner, name)
	}
}

// TestRecordItemAddressAuthorizesAgainstItsOwner is the tenant boundary the
// address move had to keep. getObject used to read `?id=owner/name`; the id is
// now the URL, and an address whose owner the authorizer cannot see is an
// address with no owner check at all.
func TestRecordItemAddressAuthorizesAgainstItsOwner(t *testing.T) {
	app := zip.New(zip.Config{})
	var owner, name string
	app.Use(zip.H(func(c *zip.Ctx) error {
		owner, name = getObject(c)
		return c.Next()
	}))
	app.Delete("/v1/records/:owner/:name", func(c *zip.Ctx) error { return c.String(200, "ok") })

	// DELETE carries no body and no query, so the address is the only place the
	// row is named — the case that used to resolve to ("", "").
	if _, err := app.Fiber().Test(httptest.NewRequest("DELETE", "/v1/records/acme/abc", nil)); err != nil {
		t.Fatal(err)
	}
	if owner != "acme" || name != "abc" {
		t.Fatalf("getObject = (%q, %q), want (acme, abc)", owner, name)
	}
}

// TestRecordItemIsRefusedToAStranger drives the same request through the REAL
// chain, which is the only way to know the resolved object is still what decides.
// A test that only calls getObject would keep passing if the route stopped being
// authorized at all.
func TestRecordItemIsRefusedToAStranger(t *testing.T) {
	app := zip.New(zip.Config{})
	Route(app)

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodDelete, "/v1/records/acme/abc", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("DELETE /v1/records/acme/abc = %d, want 403 — an anonymous caller reached another org's row", res.StatusCode)
	}
}
