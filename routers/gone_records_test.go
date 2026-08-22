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

// retired builds an app carrying only the retirement table, which is also where
// Route puts it: ahead of the filter chain, so these answers are the same with a
// credential and without one.
func retired(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	registerGone(app, goneRecords)
	return app
}

func call(t *testing.T, app *zip.App, method, path string) *http.Response {
	t.Helper()
	resp, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// TestRetiredAddressAnswersGone pins the whole answer: the status, the Link
// relation a client follows, and the two stamps that say the address is gone
// rather than going.
func TestRetiredAddressAnswersGone(t *testing.T) {
	app := retired(t)

	for address, successor := range goneRecords {
		resp := call(t, app, http.MethodGet, address)

		if resp.StatusCode != http.StatusGone {
			t.Errorf("GET %s = %d, want 410", address, resp.StatusCode)
		}
		if got, want := resp.Header.Get("Link"), "<"+successor+`>; rel="successor-version"`; got != want {
			t.Errorf("GET %s Link = %q, want %q", address, got, want)
		}
		// RFC 9745: an sf-date is "@" then the seconds. RFC 8594: an HTTP-date.
		if d := resp.Header.Get("Deprecation"); !strings.HasPrefix(d, "@") {
			t.Errorf("GET %s Deprecation = %q, want an sf-date", address, d)
		}
		if s := resp.Header.Get("Sunset"); s == "" {
			t.Errorf("GET %s has no Sunset", address)
		} else if _, err := http.ParseTime(s); err != nil {
			t.Errorf("GET %s Sunset = %q, not an HTTP-date: %v", address, s, err)
		}
	}
}

// TestRetiredBodyNamesTheSameSuccessor is why the successor is one value: a
// client reading the body and a client following the Link must be sent to the
// same address, and two statements of it can drift.
func TestRetiredBodyNamesTheSameSuccessor(t *testing.T) {
	app := retired(t)

	for address, successor := range goneRecords {
		resp := call(t, app, http.MethodGet, address)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("GET %s: %v", address, err)
		}

		var problem map[string]any
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatalf("GET %s body is not JSON: %v (%s)", address, err, body)
		}
		if got := problem["successor"]; got != successor {
			t.Errorf("GET %s body successor = %v, want %q", address, got, successor)
		}
		if got := problem["status"]; got != float64(http.StatusGone) {
			t.Errorf("GET %s body status = %v, want 410", address, got)
		}
	}
}

// TestRetiredAddressAnswersEveryMethod is the reason these are registered with
// All: 410 is a statement about the target resource, so a caller that had the
// verb wrong too still gets told where the resource went, instead of a 405
// pointing at a method that no longer exists either.
func TestRetiredAddressAnswersEveryMethod(t *testing.T) {
	app := retired(t)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		resp := call(t, app, method, "/v1/get-record")
		if resp.StatusCode != http.StatusGone {
			t.Errorf("%s /v1/get-record = %d, want 410", method, resp.StatusCode)
		}
	}
}

// TestRetiredAddressIsNotDeclared is the property zip.Undeclared exists for. A
// retired address answers every method, so publishing the table would add one
// operation per method per address to the OpenAPI document, the MCP tool list,
// the CLI and the SDKs — a contract listing calls that cannot be made.
func TestRetiredAddressIsNotDeclared(t *testing.T) {
	app := retired(t)

	if n := len(app.Registry()); n != 0 {
		t.Errorf("retirement table declared %d ops, want 0", n)
	}
	for _, r := range app.Declaration().Routes {
		if _, retired := goneRecords[r.Pattern]; retired {
			t.Errorf("%s %s is published; a retired address belongs in no projection", r.Method, r.Pattern)
		}
	}
}

// TestRetiredAddressIsGoneWithNoCredential is why the table is registered ahead
// of the filter chain, stated as a request through the real one.
//
// TestGatedRouteStillGated, next door, is the control: the same unauthenticated
// request to a LIVE route is refused, so a 410 here is the retirement answering
// rather than an inert chain. Behind the authorizer this address would answer
// 410 to a signed-in caller and 403 to everyone else — telling an anonymous
// client its URL is forbidden when the truth is that the resource moved.
func TestRetiredAddressIsGoneWithNoCredential(t *testing.T) {
	status, body := get(t, "/v1/get-record")

	if status != http.StatusGone {
		t.Fatalf("GET /v1/get-record = %d %s, want 410", status, body)
	}
	if want := goneRecords["/v1/get-record"]; !strings.Contains(body, want) {
		t.Fatalf("GET /v1/get-record body = %q, want the successor %q", body, want)
	}
}

// TestEveryRetiredRecordAddressHasALiveSuccessor closes the loop the table opens:
// a headstone pointing at an address visor does not serve is worse than no
// headstone, because the caller follows it and fails a second time.
func TestEveryRetiredRecordAddressHasALiveSuccessor(t *testing.T) {
	live := registeredRoutes(t)

	for address, successor := range goneRecords {
		var found bool
		for key := range live {
			if strings.HasSuffix(key, " "+successor) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s names successor %s, which visor does not serve", address, successor)
		}
	}
}
