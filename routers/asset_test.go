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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// assetApp installs the whole surface a request meets, retirements included, and
// no filter chain: the question these ask is which route MATCHES, and the chain
// would answer before the router's choice could be observed.
func assetApp() *zip.App {
	app := zip.New(zip.Config{})
	retire(app, goneAssets)
	registerAPI(app)
	return app
}

// TestAssetAddressIsTheOwnerAndName pins that one asset is reached by its
// (owner, name) pair and by nothing else. The proof is the SHAPE of the answer:
// the typed op refuses with a real status and no envelope, where the retired
// address answers 410.
func TestAssetAddressIsTheOwnerAndName(t *testing.T) {
	app := assetApp()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/assets"},
		{http.MethodPost, "/v1/assets"},
		{http.MethodGet, "/v1/assets/acme/db-1"},
		{http.MethodPut, "/v1/assets/acme/db-1"},
		{http.MethodDelete, "/v1/assets/acme/db-1"},
		{http.MethodPost, "/v1/assets/acme/db-1/sessions"},
		{http.MethodGet, "/v1/sessions/acme/sess-1/tunnel"},
	} {
		res, err := app.Fiber().Test(httptest.NewRequest(tc.method, tc.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d — the address is not registered", tc.method, tc.path, res.StatusCode)
		}
	}
}

// TestRetiredAssetAddressNamesItsSuccessor is the whole point of a retirement: a
// caller holding a stale address learns where the thing went, in the header and
// in the body, and the two say the same thing because they are rendered from one
// row.
func TestRetiredAssetAddressNamesItsSuccessor(t *testing.T) {
	app := assetApp()

	for path, want := range goneAssets {
		res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if res.StatusCode != http.StatusGone {
			t.Errorf("GET %s = %d, want 410 — a 404 says \"never heard of it\"", path, res.StatusCode)
			continue
		}
		for _, to := range want {
			if link := res.Header.Get("Link"); !strings.Contains(link, "<"+to+`>; rel="successor-version"`) {
				t.Errorf("GET %s: Link = %q, missing successor %s", path, link, to)
			}
		}
		if res.Header.Get("Deprecation") == "" || res.Header.Get("Sunset") == "" {
			t.Errorf("GET %s: Deprecation=%q Sunset=%q, want both stamped",
				path, res.Header.Get("Deprecation"), res.Header.Get("Sunset"))
		}

		var got notice
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("GET %s: body %s is not a notice: %v", path, body, err)
			continue
		}
		if len(got.Successor) != len(want) {
			t.Errorf("GET %s: body names %v, header names %v", path, got.Successor, want)
		}
	}
}

// TestRetiredAssetSuccessorIsServed closes the one way a retirement can lie: by
// naming an address that is not there. It reads the LIVE router, so a successor
// row and the route it points at cannot drift apart.
func TestRetiredAssetSuccessorIsServed(t *testing.T) {
	served := map[string]bool{}
	for _, r := range assetApp().Fiber().GetRoutes(true) {
		served[r.Path] = true
	}
	// The table writes RFC 6570 templates; the router spells the same address
	// with a colon.
	colon := strings.NewReplacer("{", ":", "}", "")

	for path, tos := range goneAssets {
		for _, to := range tos {
			if !served[colon.Replace(to)] {
				t.Errorf("%s names successor %s, which visor does not serve", path, to)
			}
		}
	}
}

// TestRetiredAssetAddressAnswersEveryMethod: 410 is a statement about the target
// resource, so the address is gone whatever verb reaches it. A caller that sent
// the wrong one would otherwise get 405 and no successor.
func TestRetiredAssetAddressAnswersEveryMethod(t *testing.T) {
	app := assetApp()

	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		res, err := app.Fiber().Test(httptest.NewRequest(m, "/v1/get-asset", nil))
		if err != nil {
			t.Fatalf("%s /v1/get-asset: %v", m, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusGone {
			t.Errorf("%s /v1/get-asset = %d, want 410", m, res.StatusCode)
		}
	}
}

// TestRetiredAssetAddressIsNotInTheContract: a retired address SERVES and is not
// an operation. Publishing it would put nine dead endpoints per address into the
// OpenAPI document, the MCP tool list, the CLI and every generated SDK.
func TestRetiredAssetAddressIsNotInTheContract(t *testing.T) {
	app := assetApp()

	for path := range goneAssets {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if app.Declares(m, path) {
				t.Errorf("%s %s is declared — a retirement is not part of the contract", m, path)
			}
		}
	}
	// The control: the successors ARE declared, so this is not passing because
	// nothing at all is published.
	if !app.Declares(http.MethodGet, "/v1/assets/:owner/:name") {
		t.Error("GET /v1/assets/:owner/:name is not declared — the asset ops did not register as typed")
	}
}

// TestPathCarriesTheAuthorizationObject is the load-bearing half of moving a
// family onto its resource, and the half a route diff does not show.
//
// authz.IsAllowed admits an ordinary user through one rule — subOwner ==
// objOwner — and getObject is what answers objOwner. While every address put
// its target in ?id= / ?owner= / the body, that is all getObject read. An
// address that names its resource in the PATH would have resolved to ("",""),
// and every asset route would have answered 403 for every org but `built-in`.
func TestPathCarriesTheAuthorizationObject(t *testing.T) {
	app := zip.New(zip.Config{})
	echo := func(c *zip.Ctx) error {
		owner, name := getObject(c)
		return c.JSON(http.StatusOK, [2]string{owner, name})
	}
	app.Get("/v1/assets/:owner/:name", echo)
	app.Get("/v1/get-asset", echo)

	read := func(path string) [2]string {
		t.Helper()
		res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer res.Body.Close()
		var got [2]string
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return got
	}

	if got := read("/v1/assets/acme/db-1"); got != [2]string{"acme", "db-1"} {
		t.Errorf("the path names %v, want [acme db-1] — the policy would judge an empty object", got)
	}
	// The control: an address that still carries its target in the query keeps
	// resolving from the query.
	if got := read("/v1/get-asset?id=acme/db-1"); got != [2]string{"acme", "db-1"} {
		t.Errorf("the query names %v, want [acme db-1]", got)
	}
}

// TestTunnelIsNotASessionId pins that the tunnel's address survives whatever
// spelling the SESSION collection eventually takes for its own member. The
// sessions family is migrating in parallel; a member at /v1/sessions/:id and
// this sub-resource differ in segment count, so fiber reaches each one.
func TestTunnelIsNotASessionId(t *testing.T) {
	app := zip.New(zip.Config{})
	registerTunnel(app)
	// Stand in for whatever the sessions family registers for one session.
	app.Get("/v1/sessions/:id", func(c *zip.Ctx) error { return c.String(http.StatusTeapot, "member") })

	member, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/sessions/abc", nil))
	if err != nil {
		t.Fatalf("GET /v1/sessions/abc: %v", err)
	}
	member.Body.Close()
	if member.StatusCode != http.StatusTeapot {
		t.Fatalf("GET /v1/sessions/abc = %d, want the member handler", member.StatusCode)
	}

	tunnel, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/sessions/acme/sess-1/tunnel", nil))
	if err != nil {
		t.Fatalf("GET /v1/sessions/acme/sess-1/tunnel: %v", err)
	}
	tunnel.Body.Close()
	if tunnel.StatusCode == http.StatusNotFound || tunnel.StatusCode == http.StatusTeapot {
		t.Fatalf("GET /v1/sessions/acme/sess-1/tunnel = %d — the member pattern captured the tunnel", tunnel.StatusCode)
	}
}
