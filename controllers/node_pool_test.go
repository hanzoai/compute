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

// The tenant a node-pool handler acts on is the caller's own, and the id is only
// ever a NAME. The name half of a crafted id addresses the caller's own pool of
// that name — never another org's.
func TestPoolIdIsAlwaysTheCallersOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		org, id, want string
		bad           bool
	}{
		"a bare name is a name":        {org: "acme", id: "gpu", want: "acme/gpu"},
		"whitespace is trimmed":        {org: "acme", id: "  gpu  ", want: "acme/gpu"},
		"an owner/name id is refused":  {org: "acme", id: "hanzo/gpu", bad: true},
		"the caller's own id too":      {org: "acme", id: "acme/gpu", bad: true},
		"a trailing slash is no name":  {org: "acme", id: "gpu/", bad: true},
		"no org context fails closed":  {org: "", id: "gpu", bad: true},
		"an empty name fails closed":   {org: "acme", id: "", bad: true},
		"whitespace is not a name":     {org: "acme", id: "   ", bad: true},
		"a path is not a name, either": {org: "acme", id: "a/b/c", bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := pool(caller{Owner: tc.org}, tc.id)
			if tc.bad {
				if err == nil {
					t.Fatalf("pool(%q, %q) = %q, want an error", tc.org, tc.id, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("pool(%q, %q) = %q, %v; want %q", tc.org, tc.id, got, err, tc.want)
			}
		})
	}
}

// poolWire stands the node-pool resource up on a bare app, registered exactly as
// routers.registerPools registers it. The read buffer is raised only because an
// RS256 bearer over a 2048-bit key does not fit fasthttp's 4 KiB default — the
// whole point here is to drive real requests carrying a real token.
func poolWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	zip.Get(app, "/v1/k8s/pools", ListPools)
	zip.Post(app, "/v1/k8s/pools", CreatePool)
	zip.Get(app, "/v1/k8s/pools/:id", GetPool)
	zip.Put(app, "/v1/k8s/pools/:id", ReplacePool)
	zip.Delete(app, "/v1/k8s/pools/:id", RemovePool)
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

// poolReq drives one real request at any method and returns the status with the
// body — the item ops answer with a status (404 absent, 204 removed) as much as
// with a value, so a driver that drops it cannot see what they said.
func poolReq(t *testing.T, app *zip.App, method, path, bearer, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res.StatusCode, string(b)
}

// storedPool plants a node pool belonging to owner. An empty provider leaves it
// DB-only, so nothing reaches a cloud; naming one gives it the LINKAGE — whose
// account, which cluster, which upstream pool — that a scale and a destroy read
// off the row.
func storedPool(t *testing.T, owner, name, provider string) *object.NodePool {
	t.Helper()
	now := time.Now().Format(time.RFC3339)
	p := &object.NodePool{
		Owner: owner, Name: name, OrgID: owner,
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
		MaxNodes: 4, CreatedTime: now, UpdatedTime: now,
	}
	if provider != "" {
		p.Provider, p.ClusterID, p.PoolID = provider, "cl-1", "p-1"
	}
	if _, err := object.AddNodePool(p); err != nil {
		t.Fatalf("AddNodePool(%s/%s): %v", owner, name, err)
	}
	return p
}

// A signed-in customer cannot reach another org's pool. The address names only a
// NAME now, and `?owner=` is honoured for a service subject alone — so a bearer
// naming one org and a query naming another writes the bearer's pool.
func TestReplacePoolActsOnTheCallersOwnOrgOnly(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	victim := storedPool(t, "victimorg", "gpu", "")
	storedPool(t, "attackerorg", "gpu", "")

	poolReq(t, app, http.MethodPut, "/v1/k8s/pools/gpu?owner=victimorg", mint("attackerorg"), `{"maxNodes":99}`)

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

// The address names WHICH pool, never WHOSE.
func TestRemovePoolActsOnTheCallersOwnOrgOnly(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	storedPool(t, "victimdel", "gpu", "")
	storedPool(t, "attackerdel", "gpu", "")

	status, body := poolReq(t, app, http.MethodDelete, "/v1/k8s/pools/gpu?owner=victimdel", mint("attackerdel"), "")
	if status != http.StatusNoContent {
		t.Fatalf("DELETE /v1/k8s/pools/gpu = %d %s, want 204", status, body)
	}

	if p, err := object.GetNodePool("victimdel/gpu"); err != nil || p == nil {
		t.Fatalf("another org's pool was deleted (err=%v)", err)
	}
	if p, err := object.GetNodePool("attackerdel/gpu"); err != nil || p != nil {
		t.Fatalf("the caller's own pool must be the one deleted, got %+v (err=%v)", p, err)
	}
}

// Removing a pool an org does not have is the state the caller asked for, so it
// answers 204 rather than an error nobody can act on.
func TestRemovePoolIsIdempotent(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)

	status, body := poolReq(t, app, http.MethodDelete, "/v1/k8s/pools/nosuch", mint("emptyorg"), "")
	if status != http.StatusNoContent {
		t.Fatalf("DELETE of an absent pool = %d %s, want 204", status, body)
	}
}

// Reading a pool an org does not have is 404, not a 200 carrying null. A caller
// that has to look inside a success to find a miss is one that will forget to.
func TestGetPoolOfAnAbsentPoolIs404(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)

	status, body := poolReq(t, app, http.MethodGet, "/v1/k8s/pools/nosuch", mint("emptyread"), "")
	if status != http.StatusNotFound {
		t.Fatalf("GET of an absent pool = %d %s, want 404", status, body)
	}
}

// COUNT IS OPTIONAL, and absent means unchanged. Reaching a count spends money at
// the provider, so a request that only edits the autoscale bounds must not read
// as "scale to nothing" — this pool has no provider linkage at all, so a scale
// attempt would fail loudly rather than silently succeed.
func TestReplacePoolWithoutACountTouchesNoProvider(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	storedPool(t, "boundsorg", "gpu", "")

	status, body := poolReq(t, app, http.MethodPut, "/v1/k8s/pools/gpu", mint("boundsorg"),
		`{"minNodes":2,"maxNodes":8,"autoScale":true}`)
	if status != http.StatusOK {
		t.Fatalf("PUT bounds only = %d %s, want 200", status, body)
	}

	after, err := object.GetNodePool("boundsorg/gpu")
	if err != nil || after == nil {
		t.Fatalf("read back: %v", err)
	}
	if after.MinNodes != 2 || after.MaxNodes != 8 || !after.AutoScale {
		t.Fatalf("the bounds must be written, got %+v", after)
	}
	if after.Count != 4 {
		t.Fatalf("an absent count must leave the pool at its provider's count, got %d", after.Count)
	}
}

// A stated count is the provider's to reach, and a pool with no provider pool
// behind it has nothing to scale — said as a refusal, not as a silent 200 that
// leaves the caller believing it resized something.
func TestReplacePoolWithACountNeedsAProviderPool(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	storedPool(t, "scalenolink", "gpu", "")

	status, body := poolReq(t, app, http.MethodPut, "/v1/k8s/pools/gpu", mint("scalenolink"), `{"count":8}`)
	if status == http.StatusOK {
		t.Fatalf("PUT count on a pool with no provider pool = 200 %s, want a refusal", body)
	}
	if !strings.Contains(body, "no provider pool") {
		t.Fatalf("the refusal must say what is missing, got %s", body)
	}
}

// Every write refuses outright when there is no org context: no bearer and no
// service credential means no tenant, and a tenant-less provision is a platform
// account waiting to happen.
func TestNodePoolWritesFailClosedWithoutAnOrg(t *testing.T) {
	app := poolWire(t)
	for name, tc := range map[string]struct{ method, path, body string }{
		"create":  {http.MethodPost, "/v1/k8s/pools", `{"provider":"do","size":"gpu-h100x8-640gb","count":1}`},
		"replace": {http.MethodPut, "/v1/k8s/pools/gpu", `{"maxNodes":9}`},
		"scale":   {http.MethodPut, "/v1/k8s/pools/gpu", `{"count":4}`},
		"remove":  {http.MethodDelete, "/v1/k8s/pools/gpu", ``},
	} {
		t.Run(name, func(t *testing.T) {
			_, body := poolReq(t, app, tc.method, tc.path, "", tc.body)
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

	// A scale reads WHICH provider account off the stored row, so the row is what
	// carries the linkage a scale resolves.
	storedPool(t, "acmeprov", "gpu", "platformdo")

	for name, tc := range map[string]struct{ method, path, body string }{
		"create": {http.MethodPost, "/v1/k8s/pools?owner=hanzo", `{"provider":"platformdo","size":"gpu-h100x8-640gb","count":8}`},
		"scale":  {http.MethodPut, "/v1/k8s/pools/gpu?owner=hanzo", `{"count":8}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, body := poolReq(t, app, tc.method, tc.path, mint("acmeprov"), tc.body)
			if !strings.Contains(body, "acmeprov") {
				t.Fatalf("the provision must resolve the provider in the CALLER's org, got %s", body)
			}
			if strings.Contains(body, "hanzo") {
				t.Fatalf("the query's org reached the provision: %s", body)
			}
		})
	}
}

// The READS take their tenant from the caller too. Authorization used to key a
// GET on the very ?owner= the handler read, so the two agreed and the caller
// chose both — a customer listing another org's pools was one query parameter
// away, and the agreement is exactly what hid it.
func TestNodePoolReadsAreScopedToTheCaller(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := poolWire(t)
	storedPool(t, "victimread", "secret-gpu", "")
	storedPool(t, "attackerread", "own-gpu", "")

	list := get(t, app, "/v1/k8s/pools?owner=victimread", mint("attackerread"))
	if strings.Contains(list, "secret-gpu") || strings.Contains(list, "victimread") {
		t.Fatalf("another org's pools were listed: %s", list)
	}
	if !strings.Contains(list, "own-gpu") {
		t.Fatalf("the caller must still see its OWN pools: %s", list)
	}

	_, one := poolReq(t, app, http.MethodGet, "/v1/k8s/pools/secret-gpu?owner=victimread", mint("attackerread"), "")
	if strings.Contains(one, "secret-gpu") || strings.Contains(one, "victimread") {
		t.Fatalf("another org's pool was readable by id: %s", one)
	}
}

// A read with no org context is refused rather than served somebody's rows.
func TestNodePoolReadsFailClosedWithoutAnOrg(t *testing.T) {
	app := poolWire(t)
	for name, path := range map[string]string{
		"list": "/v1/k8s/pools",
		"get":  "/v1/k8s/pools/gpu",
	} {
		t.Run(name, func(t *testing.T) {
			if body := get(t, app, path, ""); !strings.Contains(body, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}
}
