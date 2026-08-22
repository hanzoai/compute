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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// send drives one real request of any verb through a fully-routed app. Through
// Route rather than registerAPI, because where a retired address sits in the
// filter chain is half of what these tests measure.
func send(t *testing.T, method, path string) (*http.Response, string) {
	t.Helper()
	app := zip.New(zip.Config{})
	Route(app)

	res, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res, string(b)
}

// TestRetiredAddressNamesItsSuccessor is the whole contract of a retirement: the
// address is finished, and the caller is told where the resource went — in a
// header a client can follow without parsing, and in the body, from the same row.
//
// A bare 404 would say the address never existed, and a 301 would say the request
// can be repeated somewhere else, which it cannot: /v1/sizes?gpu=true answers a
// different shape than /v1/gpus did.
func TestRetiredAddressNamesItsSuccessor(t *testing.T) {
	for address, successor := range goneCatalog {
		res, body := send(t, http.MethodGet, address)

		if res.StatusCode != http.StatusGone {
			t.Errorf("GET %s = %d, want 410", address, res.StatusCode)
		}
		if got, want := res.Header.Get("Link"), "<"+successor+`>; rel="successor-version"`; got != want {
			t.Errorf("GET %s Link = %q, want %q", address, got, want)
		}
		if got := res.Header.Get("Deprecation"); !strings.HasPrefix(got, "@") {
			t.Errorf("GET %s Deprecation = %q, want an @-prefixed unix date (RFC 9745)", address, got)
		}
		if got := res.Header.Get("Sunset"); !strings.HasSuffix(got, "GMT") {
			t.Errorf("GET %s Sunset = %q, want an HTTP-date (RFC 8594)", address, got)
		}
		if !strings.Contains(body, successor) {
			t.Errorf("GET %s body = %s, want it to name %s", address, body, successor)
		}
	}
}

// TestRetiredAddressAnswersEveryMethod: 410 is a statement about the target
// resource, not about a call. A caller that sent the wrong verb to a retired
// address still needs the successor, and a 405 would send them looking for the
// right verb on an address that has none.
//
// OPTIONS is deliberately absent: a preflight is a different question, answered
// by the CORS middleware ahead of every route.
func TestRetiredAddressAnswersEveryMethod(t *testing.T) {
	for address := range goneCatalog {
		for _, method := range []string{
			http.MethodGet, http.MethodPost, http.MethodPut,
			http.MethodPatch, http.MethodDelete,
		} {
			if res, body := send(t, method, address); res.StatusCode != http.StatusGone {
				t.Errorf("%s %s = %d %s, want 410", method, address, res.StatusCode, body)
			}
		}
	}
}

// TestRetiredAddressIsUndeclared is why these are registered on zip.Undeclared.
//
// They SERVE — the test above proves it — and they appear in nothing built from
// the declaration: the OpenAPI document, the MCP tool list, the CLI, the SDKs.
// Published, each would be one dead operation per method, most of them calls that
// never existed, in the contract a customer reads.
func TestRetiredAddressIsUndeclared(t *testing.T) {
	app := zip.New(zip.Config{})
	Route(app)

	for address := range goneCatalog {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if app.Declares(method, address) {
				t.Errorf("%s %s is declared — a retired address must serve and publish nothing", method, address)
			}
		}
	}

	// The control: without it, "not declared" would pass just as well against an
	// app that declares nothing at all.
	if !app.Declares(http.MethodGet, "/v1/account") {
		t.Fatal("GET /v1/account is not declared, so this test cannot tell a retirement from an empty declaration")
	}
}

// TestSuccessorIsServed closes the direction the header cannot: a retirement that
// names an address visor does not serve sends the caller from a 410 to a 404,
// which is worse than saying nothing.
func TestSuccessorIsServed(t *testing.T) {
	served := registeredRoutes(t)
	for address, successor := range goneCatalog {
		path, _, _ := strings.Cut(successor, "?")
		if !served["GET "+path] {
			t.Errorf("%s names %s, which visor does not serve", address, successor)
		}
	}
}
