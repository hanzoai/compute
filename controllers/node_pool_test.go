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

package controllers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// The tenant a node-pool handler acts on is the caller's own, and the id is
// only ever a NAME. An id naming another org addresses the caller's own pool of
// that name — never the other org's — so the historical `owner/name` form keeps
// working without carrying a tenant.
func TestPoolIdIsAlwaysTheCallersOwn(t *testing.T) {
	for name, tc := range map[string]struct{ org, id, want string }{
		"owner/name keeps only the name": {"acme", "acme/gpu", "acme/gpu"},
		"a foreign owner is discarded":   {"acme", "hanzo/gpu", "acme/gpu"},
		"a bare name is a name":          {"acme", "gpu", "acme/gpu"},
		"whitespace is trimmed":          {"acme", "  acme/gpu  ", "acme/gpu"},
		"no org context fails closed":    {"", "acme/gpu", ""},
		"no name fails closed":           {"acme", "", ""},
		"a trailing slash is no name":    {"acme", "acme/", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := poolId(tc.org, tc.id); got != tc.want {
				t.Fatalf("poolId(%q, %q) = %q, want %q", tc.org, tc.id, got, tc.want)
			}
		})
	}
}

// poolWire stands the node-pool lifecycle up on a bare app, registered exactly as
// routers.Route registers it. The read buffer is raised only because an RS256
// bearer over a 2048-bit key does not fit fasthttp's 4 KiB default — the whole
// point here is to drive real requests carrying a real token.
func poolWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	handler := func(fn func(*ApiController)) zip.Handler {
		return func(c *zip.Ctx) error { fn(New(c)); return nil }
	}
	app.Get("/v1/get-node-pools", handler((*ApiController).GetNodePools))
	app.Get("/v1/get-node-pool", handler((*ApiController).GetNodePool))
	app.Post("/v1/update-node-pool", handler((*ApiController).UpdateNodePool))
	app.Post("/v1/delete-node-pool", handler((*ApiController).DeleteNodePool))
	app.Post("/v1/create-node-pool", handler((*ApiController).CreateNodePool))
	app.Post("/v1/scale-node-pool", handler((*ApiController).ScaleNodePool))
	return app
}

// post drives one real request, with an optional bearer, and returns the body.
func post(t *testing.T, app *zip.App, path, bearer, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("POST %s: read body: %v", path, err)
	}
	return string(b)
}

// get drives one real GET, with an optional bearer, and returns the body.
func get(t *testing.T, app *zip.App, path, bearer string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return string(b)
}

// storedPool plants a DB-only node pool (no provider linkage, so nothing reaches
// a cloud) belonging to owner.
func storedPool(t *testing.T, owner, name string) *object.NodePool {
	t.Helper()
	now := time.Now().Format(time.RFC3339)
	p := &object.NodePool{
		Owner: owner, Name: name, OrgID: owner,
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
		MaxNodes: 4, CreatedTime: now, UpdatedTime: now,
	}
	if _, err := object.AddNodePool(p); err != nil {
		t.Fatalf("AddNodePool(%s/%s): %v", owner, name, err)
	}
	return p
}

// A signed-in customer cannot reach another org's pool by naming it in `?id=`.
// The handler took the tenant from the id while the authorization filter took it
// from the id too — so the two agreed, and both were the caller's to choose.
func TestUpdateNodePoolActsOnTheCallersOwnOrgOnly(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	victim := storedPool(t, "victimorg", "gpu")
	storedPool(t, "attackerorg", "gpu")

	post(t, app, "/v1/update-node-pool?owner=victimorg&id=victimorg/gpu", mint("attackerorg"),
		`{"owner":"victimorg","name":"gpu","maxNodes":99}`)

	after, err := object.GetNodePool("victimorg/gpu")
	if err != nil || after == nil {
		t.Fatalf("read back victim: %v", err)
	}
	if after.MaxNodes != victim.MaxNodes {
		t.Fatalf("another org's pool was edited: maxNodes %d -> %d", victim.MaxNodes, after.MaxNodes)
	}
	// The edit landed on the CALLER's own pool of that name, which is the whole
	// point: the request is not refused, it is re-scoped to the caller.
	own, err := object.GetNodePool("attackerorg/gpu")
	if err != nil || own == nil {
		t.Fatalf("read back caller's own: %v", err)
	}
	if own.MaxNodes != 99 {
		t.Fatalf("the caller's own pool must take the edit, got maxNodes=%d", own.MaxNodes)
	}
}

// The delete body names WHICH pool, never WHOSE.
func TestDeleteNodePoolActsOnTheCallersOwnOrgOnly(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	storedPool(t, "victimdel", "gpu")
	storedPool(t, "attackerdel", "gpu")

	post(t, app, "/v1/delete-node-pool", mint("attackerdel"), `{"owner":"victimdel","name":"gpu"}`)

	if p, err := object.GetNodePool("victimdel/gpu"); err != nil || p == nil {
		t.Fatalf("another org's pool was deleted (err=%v)", err)
	}
	if p, err := object.GetNodePool("attackerdel/gpu"); err != nil || p != nil {
		t.Fatalf("the caller's own pool must be the one deleted, got %+v (err=%v)", p, err)
	}
}

// Every write refuses outright when there is no org context: no bearer and no
// service credential means no tenant, and a tenant-less provision is a house
// account waiting to happen.
func TestNodePoolWritesFailClosedWithoutAnOrg(t *testing.T) {
	app := poolWire(t)
	for name, tc := range map[string]struct{ path, body string }{
		"create": {"/v1/create-node-pool?provider=do", `{"size":"gpu-h100x8-640gb","count":1}`},
		"scale":  {"/v1/scale-node-pool?provider=do&poolId=p-1&count=4", ``},
		"update": {"/v1/update-node-pool", `{"maxNodes":9}`},
		"delete": {"/v1/delete-node-pool", `{"name":"gpu"}`},
	} {
		t.Run(name, func(t *testing.T) {
			body := post(t, app, tc.path, "", tc.body)
			if !strings.Contains(body, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}
}

// The provision paths take their org from the signed claim, so `?owner=` cannot
// point the provision at another tenant's provider credentials, balance and
// invoice. They fail on the missing PROVIDER — which is proof they got past the
// tenant resolution carrying the caller's own org, and looked it up there.
func TestProvisionUsesTheSignedOrgNotTheQuery(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)

	for name, path := range map[string]string{
		"create": "/v1/create-node-pool?owner=hanzo&provider=housedo",
		"scale":  "/v1/scale-node-pool?owner=hanzo&provider=housedo&poolId=p-1&count=8",
	} {
		t.Run(name, func(t *testing.T) {
			body := post(t, app, path, mint("acmeprov"), `{"owner":"hanzo","size":"gpu-h100x8-640gb","count":8}`)
			if !strings.Contains(body, "acmeprov") {
				t.Fatalf("the provision must resolve the provider in the CALLER's org, got %s", body)
			}
			if strings.Contains(body, "hanzo") {
				t.Fatalf("the query's org reached the provision: %s", body)
			}
		})
	}
}

// The READS take their tenant from the caller too. Authorization keys a GET on
// the very ?owner= the handler used to read, so the two agreed and the caller
// chose both — a customer listing another org's pools was one query parameter
// away, and the agreement is exactly what hid it.
func TestNodePoolReadsAreScopedToTheCaller(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	storedPool(t, "victimread", "secret-gpu")
	storedPool(t, "attackerread", "own-gpu")

	list := get(t, app, "/v1/get-node-pools?owner=victimread", mint("attackerread"))
	if strings.Contains(list, "secret-gpu") || strings.Contains(list, "victimread") {
		t.Fatalf("another org's pools were listed: %s", list)
	}
	if !strings.Contains(list, "own-gpu") {
		t.Fatalf("the caller must still see its OWN pools: %s", list)
	}

	one := get(t, app, "/v1/get-node-pool?owner=victimread&id=victimread/secret-gpu", mint("attackerread"))
	if strings.Contains(one, "secret-gpu") || strings.Contains(one, "victimread") {
		t.Fatalf("another org's pool was readable by id: %s", one)
	}
}

// A read with no org context is refused rather than served somebody's rows.
func TestNodePoolReadsFailClosedWithoutAnOrg(t *testing.T) {
	app := poolWire(t)
	for name, path := range map[string]string{
		"list": "/v1/get-node-pools",
		"get":  "/v1/get-node-pool?id=gpu",
	} {
		t.Run(name, func(t *testing.T) {
			if body := get(t, app, path, ""); !strings.Contains(body, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}
}
