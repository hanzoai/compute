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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// send drives one real request of any method through a fully-routed app. Through
// Route rather than registerGone, because where a retirement sits in the filter
// chain is half of what these tests are about.
func send(t *testing.T, method, path string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{})
	Route(app)

	res, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// retired is one address from the table, used as the subject of these tests. The
// row is read from goneMachines rather than restated, so a test cannot go on
// asserting about an address that is no longer retired.
func retired(t *testing.T) (string, string) {
	t.Helper()
	for path, successor := range goneMachines {
		return path, successor
	}
	t.Fatal("no retired address to test")
	return "", ""
}

// TestRetiredAddressNamesItsSuccessor is the whole contract of a retirement: the
// address is gone, and the answer says where the resource went — in the headers
// a generic client follows and in the body a person reads, from the one value
// the table holds, so the two cannot disagree.
func TestRetiredAddressNamesItsSuccessor(t *testing.T) {
	path, successor := retired(t)
	res := send(t, http.MethodPost, path)
	defer res.Body.Close()

	if res.StatusCode != http.StatusGone {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("POST %s = %d %s, want 410", path, res.StatusCode, b)
	}

	// RFC 5829: the replacement, under a registered relation, so a client that
	// was never taught anything about visor can follow it.
	// Link is a list header, so the successor is one of several values.
	want := "<" + successor + `>; rel="successor-version"`
	if !slices.Contains(res.Header.Values("Link"), want) {
		t.Errorf("Link = %v, want a member %q", res.Header.Values("Link"), want)
	}

	// RFC 9745: a structured-field Date, which is "@" and the seconds. RFC 8594:
	// an HTTP-date. Both are NOW — the address is gone, not going — so both must
	// parse and neither may be in the future.
	dep := res.Header.Get("Deprecation")
	secs, err := strconv.ParseInt(strings.TrimPrefix(dep, "@"), 10, 64)
	if !strings.HasPrefix(dep, "@") || err != nil {
		t.Fatalf("Deprecation = %q, want @<seconds>", dep)
	}
	if time.Unix(secs, 0).After(time.Now().Add(time.Minute)) {
		t.Errorf("Deprecation = %q, which is in the future; the address is gone, not going", dep)
	}
	sunset, err := time.Parse(http.TimeFormat, res.Header.Get("Sunset"))
	if err != nil {
		t.Fatalf("Sunset = %q: %v", res.Header.Get("Sunset"), err)
	}
	if sunset.After(time.Now().Add(time.Minute)) {
		t.Errorf("Sunset = %q, which is in the future; the address is gone, not going", sunset)
	}

	// RFC 9457: the refusal speaks the vocabulary an HTTP API answers in, and
	// carries the successor as an extension member.
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var body struct {
		Status    int    `json:"status"`
		Successor string `json:"successor"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != http.StatusGone {
		t.Errorf("body status = %d, want 410", body.Status)
	}
	if body.Successor != successor {
		t.Errorf("body successor = %q, header names %q — one row, two answers", body.Successor, successor)
	}
}

// TestRetiredAddressAnswersEveryMethod: 410 is a statement about the target
// resource, so a caller who also got the verb wrong still gets the successor. A
// 404 or a 405 here would send them looking for a typo instead.
func TestRetiredAddressAnswersEveryMethod(t *testing.T) {
	path, _ := retired(t)
	for _, m := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete,
	} {
		res := send(t, m, path)
		res.Body.Close()
		if res.StatusCode != http.StatusGone {
			t.Errorf("%s %s = %d, want 410", m, path, res.StatusCode)
		}
	}
}

// TestRetiredAddressReachesTheAnswerUnauthenticated is why registerGone is called
// ahead of the filter chain. No credential admits a resource that does not
// exist, so there is nothing to authorize — and behind the policy engine a
// caller holding a stale address is told 403 and never learns the successor.
// TestGatedRouteStillGated is the control: a live route is still gated.
func TestRetiredAddressReachesTheAnswerUnauthenticated(t *testing.T) {
	path, _ := retired(t)
	res := send(t, http.MethodPost, path)
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden {
		t.Fatalf("POST %s = 403 — the authorization answer hid the routing one", path)
	}
	if res.StatusCode != http.StatusGone {
		t.Fatalf("POST %s = %d, want 410", path, res.StatusCode)
	}
}

// TestRetiredAddressIsNotDeclared: a retirement SERVES and is not part of the
// contract. Declaration is what every projection is built from — the OpenAPI
// document, the MCP tool list, the CLI, the by-name call plane — and a retired
// address answers every method, so publishing one would put a row of dead
// operations into the surface customers read.
func TestRetiredAddressIsNotDeclared(t *testing.T) {
	app := zip.New(zip.Config{})
	Route(app)

	for path := range goneMachines {
		for _, r := range app.Declaration().Routes {
			if r.Pattern == path {
				t.Errorf("%s %s is declared; a retired address must not be", r.Method, path)
			}
		}
	}
	// The control: the successor IS declared, so this test cannot pass by the
	// declaration being empty.
	if !app.Declares(http.MethodPost, "/v1/machines") {
		t.Error("POST /v1/machines is not declared, so the assertion above proves nothing")
	}
}
