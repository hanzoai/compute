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
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/pkg/gone"
)

// These drive registerAPI alone, with no filter chain: the question is what the
// ROUTER does with an address, and the chain would answer before the router's
// choice could be observed.

// answersGone asserts that path answers 410 for method and names to as its
// successor in BOTH the Link header and the body, with the two RFC stamps
// beside it. It returns nothing: every failure is reported here, so a caller is
// one line.
func answersGone(t *testing.T, app *zip.App, method, path, to string) {
	t.Helper()

	res, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusGone {
		t.Fatalf("%s %s = %d, want 410 — a retired address is not a 404, which a "+
			"client reads as a typo and retries", method, path, res.StatusCode)
	}

	// Link is a LIST header (RFC 8288 §3): zip adds self/service-desc/service-doc
	// of its own, so the successor is one value among several, not the only one.
	want := "<" + to + `>; rel="successor-version"`
	if links := res.Header.Values("Link"); !slices.Contains(links, want) {
		t.Errorf("%s %s Link = %q, want one of them to be %q", method, path, links, want)
	}

	// RFC 9745: an sf-date, "@" then unix seconds. RFC 8594: http.TimeFormat.
	// Both read NOW — the address is gone, not going — so a future stamp would
	// promise a grace period nothing keeps.
	dep := res.Header.Get("Deprecation")
	secs, err := strconv.ParseInt(strings.TrimPrefix(dep, "@"), 10, 64)
	if !strings.HasPrefix(dep, "@") || err != nil {
		t.Errorf("%s %s Deprecation = %q, want an sf-date (@unix)", method, path, dep)
	} else if d := time.Since(time.Unix(secs, 0)); d < 0 || d > time.Minute {
		t.Errorf("%s %s Deprecation is %v away from now, want now", method, path, d)
	}
	if sunset := res.Header.Get("Sunset"); sunset == "" {
		t.Errorf("%s %s has no Sunset header", method, path)
	} else if _, err := time.Parse(http.TimeFormat, sunset); err != nil {
		t.Errorf("%s %s Sunset = %q, want http.TimeFormat: %v", method, path, sunset, err)
	}

	// The body and the header render the same row, so they cannot disagree —
	// this is what asserts that, rather than trusting the code that says it.
	var got gone.Answer
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("%s %s body: %v", method, path, err)
	}
	if got.Gone != path || got.Successor != to {
		t.Errorf("%s %s body = %+v, want {Gone:%s Successor:%s}", method, path, got, path, to)
	}
}

// TestPlanVerbsAreGone pins the retirement of the five addresses that carried
// the operation in the path.
//
// Every one answers on a method it NEVER had, and that is the point rather than
// an accident: 410 is a statement about the target resource (RFC 9110
// §15.5.11), and a caller who sent the wrong verb is exactly the caller who
// needs to be told where the resource went.
func TestPlanVerbsAreGone(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	for from, to := range gonePlans {
		answersGone(t, app, http.MethodGet, from, to)
		answersGone(t, app, http.MethodPost, from, to)
		answersGone(t, app, http.MethodPatch, from, to)
	}
}

// TestRetiredPlanVerbsAreNotPublished is the other half, and the reason those
// addresses are registered on zip.Undeclared: they SERVE but are absent from
// App.Declaration, so no projection built from it — the OpenAPI document, the
// MCP tool list, the CLI, the by-name call plane — offers a caller a dead
// endpoint. Published, five addresses answering every method would be forty
// operations in the customer contract.
func TestRetiredPlanVerbsAreNotPublished(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	for from := range gonePlans {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if app.Declares(m, from) {
				t.Errorf("%s %s is retired but still published in the declaration", m, from)
			}
		}
	}
}

// TestPlanIsOneAddress is the control. Without it the retirement tests pass just
// as well when the plan surface was deleted outright rather than moved, which is
// the failure they are least able to notice on their own.
//
// The proof is the SHAPE of the refusal: these are typed ops, so an unaddressed
// catalog is a real 400 with no envelope, where an untyped controller method
// would answer 200 and put its verdict in the body.
func TestPlanIsOneAddress(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	// pattern is what the router matched on and what the declaration names; path
	// is one request against it.
	for _, r := range []struct{ method, pattern, path string }{
		{http.MethodGet, "/v1/plans", "/v1/plans"},
		{http.MethodPost, "/v1/plans", "/v1/plans"},
		{http.MethodGet, "/v1/plans/:name", "/v1/plans/starter"},
		{http.MethodPut, "/v1/plans/:name", "/v1/plans/starter"},
		{http.MethodDelete, "/v1/plans/:name", "/v1/plans/starter"},
	} {
		if !app.Declares(r.method, r.pattern) {
			t.Errorf("%s %s is not published — the contract cannot be read", r.method, r.pattern)
		}
		res, err := app.Fiber().Test(httptest.NewRequest(r.method, r.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", r.method, r.path, err)
		}
		res.Body.Close()
		// No ?owner, so no catalog is addressed and the op refuses before it
		// reaches a store. 404 would mean the route is gone; 200 would mean an
		// enveloped handler answered.
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400 from the typed op", r.method, r.path, res.StatusCode)
		}
	}
}
