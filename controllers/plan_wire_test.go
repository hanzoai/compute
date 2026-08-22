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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// These pin the WIRE of the plan ops against a REAL store (the Base backend
// TestMain installs), because the five addresses moved and the envelope went
// with them — neither of which a compiler checks.
//
// The old wire was casibase's: HTTP 200 always, with {status,msg,data} around
// the value, "Affected"/"Unaffected" for a write, and `data: null` for a read
// that found nothing. A typed op has no envelope: the answer IS the value and
// the status IS the outcome.

// plans stands the five ops up on a bare app, at the addresses registerPlans
// uses. No filter chain — the question here is what a handler ANSWERS.
func plans(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/plans", ListPlans)
	zip.Post(app, "/v1/plans", CreatePlan)
	zip.Get(app, "/v1/plans/:name", GetPlan)
	zip.Put(app, "/v1/plans/:name", ReplacePlan)
	zip.Delete(app, "/v1/plans/:name", DeletePlan)
	return app
}

// send drives one real request, with a body when there is one, and returns the
// status and the raw body.
func send(t *testing.T, app *zip.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
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

// TestPlanRoundTrip drives one plan through every op at its new address, in the
// order a caller would: create it, find it in its catalog, read it, replace it,
// remove it, and read the absence.
//
// One test rather than five, deliberately: what is being pinned is that the five
// ops address the SAME resource, and five tests each proving one op in isolation
// prove exactly the thing that was already true before the migration.
func TestPlanRoundTrip(t *testing.T) {
	app := plans(t)
	mine := org(t)
	at := "/v1/plans/starter?owner=" + mine

	status, body := send(t, app, http.MethodPost, "/v1/plans",
		`{"owner":"`+mine+`","name":"starter","displayName":"Starter","state":"Active","priceMonthly":500}`)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/plans = %d %s, want 200", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("POST /v1/plans answered the casibase envelope: %s", body)
	}

	// The collection answers an OBJECT keyed `plans`, not a bare array: an array
	// has nowhere to put a count or a cursor later without breaking every client.
	status, body = send(t, app, http.MethodGet, "/v1/plans?owner="+mine, "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/plans = %d %s, want 200", status, body)
	}
	var list Plans
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("GET /v1/plans body %s: %v", body, err)
	}
	if len(list.Plans) != 1 || list.Plans[0].Name != "starter" {
		t.Fatalf("GET /v1/plans = %s, want the one plan just created", body)
	}

	status, body = send(t, app, http.MethodGet, at, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", at, status, body)
	}
	var got object.Plan
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("GET %s body %s: %v", at, body, err)
	}
	if got.DisplayName != "Starter" || got.PriceMonthly != 500 {
		t.Fatalf("GET %s = %+v, want the plan as created", at, got)
	}

	// The URL is the addressing authority: this body names a different plan in a
	// different catalog, and the plan the URL named is the one that changes.
	status, body = send(t, app, http.MethodPut, at,
		`{"owner":"someone-else","name":"other","displayName":"Starter Plus","state":"Active","priceMonthly":900}`)
	if status != http.StatusOK {
		t.Fatalf("PUT %s = %d %s, want 200", at, status, body)
	}
	status, body = send(t, app, http.MethodGet, at, "")
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("GET %s body %s: %v", at, body, err)
	}
	if got.Owner != mine || got.Name != "starter" {
		t.Fatalf("PUT %s wrote %s/%s — the body outranked the URL", at, got.Owner, got.Name)
	}
	if got.DisplayName != "Starter Plus" || got.PriceMonthly != 900 {
		t.Fatalf("GET %s after replace = %+v, want the replacement", at, got)
	}

	if status, body = send(t, app, http.MethodDelete, at, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE %s = %d %s, want 204", at, status, body)
	} else if body != "" {
		t.Fatalf("DELETE %s answered a body %q, want none", at, body)
	}

	// Absent is 404, not a 200 carrying null: a caller that has to inspect the
	// fields of a success to learn it got nothing is one that will forget to.
	if status, body = send(t, app, http.MethodGet, at, ""); status != http.StatusNotFound {
		t.Fatalf("GET %s (removed) = %d %s, want 404", at, status, body)
	}
}

// TestReplaceOfAnAbsentPlanIs404 pins the write that used to land nowhere and
// say so was fine. object.UpdatePlan reports success whether or not a row
// matched, so without this the caller is told its price change took effect.
func TestReplaceOfAnAbsentPlanIs404(t *testing.T) {
	app := plans(t)
	at := "/v1/plans/nothing-here?owner=" + org(t)

	status, body := send(t, app, http.MethodPut, at, `{"displayName":"Ghost","priceMonthly":1}`)
	if status != http.StatusNotFound {
		t.Fatalf("PUT %s = %d %s, want 404", at, status, body)
	}
}

// TestAnUnaddressedCatalogIs400 pins the refusal, and it is the reason `?owner`
// is required rather than defaulted: /v1/plans with no catalog named addresses
// nothing, and answering an empty list would say a catalog exists and is empty.
func TestAnUnaddressedCatalogIs400(t *testing.T) {
	app := plans(t)

	if status, body := send(t, app, http.MethodGet, "/v1/plans", ""); status != http.StatusBadRequest {
		t.Fatalf("GET /v1/plans (no owner) = %d %s, want 400", status, body)
	}
}

// TestACatalogIsNotAnothersToTouch is the tenant boundary on this noun, and the
// ops are called DIRECTLY rather than over the wire because that is the door
// being tested: a typed op is reachable BY NAME — zip.Here, an MCP tool, the
// call plane — where there is no request for ApiFilter to authorize, and the
// only thing standing between a caller and another org's catalog is `plan()`.
//
// It also closes the seam the URL's authority opens on the HTTP door: ApiFilter
// reads the OWNER out of a write's BODY, while the op takes it from the URL, so
// a body naming the caller's own org authorizes a write the URL aims elsewhere.
// The guard judges what is about to be WRITTEN, so the two cannot diverge.
func TestACatalogIsNotAnothersToTouch(t *testing.T) {
	mint := signer(t, "https://test.id")
	bearer := mint("mine")
	ctx := context.Background()
	theirs := caller{Owner: "victim", Authorization: bearer}

	refused := func(name string, err error) {
		t.Helper()
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Errorf("%s on another org's catalog = %v, want 403", name, err)
		}
	}

	_, err := ListPlans(ctx, &Scope{theirs})
	refused("ListPlans", err)
	_, err = GetPlan(ctx, &PlanRef{Name: "starter", caller: theirs})
	refused("GetPlan", err)
	_, err = DeletePlan(ctx, &PlanRef{Name: "starter", caller: theirs})
	refused("DeletePlan", err)
	// The body names the caller's OWN org; the URL names the victim's. Refusing
	// this is the whole point of judging the write rather than the claim.
	write := &PlanWrite{
		Plan:          object.Plan{Owner: "victim", Name: "starter", DisplayName: "mine now"},
		Authorization: bearer,
	}
	_, err = CreatePlan(ctx, write)
	refused("CreatePlan", err)
	_, err = ReplacePlan(ctx, write)
	refused("ReplacePlan", err)
}
