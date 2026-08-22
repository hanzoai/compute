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

// gone stands the retirements up on a bare app and drives one request.
func gone(t *testing.T, method, path string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{})
	retireSessions(app)
	res, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// TestARetiredSessionAddressNamesItsSuccessor is the point of the whole table: a
// caller holding a dead address is told where the thing went, in the header and
// in the body, and the two are rendered from one row so they cannot disagree.
func TestARetiredSessionAddressNamesItsSuccessor(t *testing.T) {
	for path, want := range goneSessions {
		res := gone(t, http.MethodGet, path)
		defer res.Body.Close()

		if res.StatusCode != http.StatusGone {
			t.Errorf("GET %s = %d, want 410", path, res.StatusCode)
			continue
		}
		for _, to := range want {
			if link := res.Header.Get("Link"); !strings.Contains(link, "<"+to+`>; rel="successor-version"`) {
				t.Errorf("GET %s Link = %q, want a successor-version link to %s", path, link, to)
			}
		}
		if d := res.Header.Get("Deprecation"); !strings.HasPrefix(d, "@") {
			t.Errorf("GET %s Deprecation = %q, want RFC 9745's @<unix>", path, d)
		}
		if s := res.Header.Get("Sunset"); s == "" {
			t.Errorf("GET %s carries no Sunset", path)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("GET %s: read body: %v", path, err)
		}
		var got struct {
			Successor []string `json:"successor"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("GET %s: decode %s: %v", path, body, err)
		}
		if strings.Join(got.Successor, ",") != strings.Join(want, ",") {
			t.Errorf("GET %s body successor = %v, want %v", path, got.Successor, want)
		}
	}
}

// TestARetiredSessionAddressIsGoneForEveryMethod — 410 is a statement about the
// target resource, so the address is gone whatever verb reaches it. Naming
// methods would leave a caller that sent the wrong one with a 405 and no
// successor, which is the caller most in need of one.
func TestARetiredSessionAddressIsGoneForEveryMethod(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		res := gone(t, m, "/v1/get-sessions")
		defer res.Body.Close()
		if res.StatusCode != http.StatusGone {
			t.Errorf("%s /v1/get-sessions = %d, want 410", m, res.StatusCode)
		}
	}
}

// TestARetiredSessionAddressIsNotInTheContract keeps the dead addresses out of
// every projection built from the declaration — the OpenAPI document, the MCP
// tool list, the CLI, the by-name call plane. They SERVE; they are not part of
// what visor offers.
func TestARetiredSessionAddressIsNotInTheContract(t *testing.T) {
	app := zip.New(zip.Config{})
	retireSessions(app)
	registerAPI(app)

	for _, r := range app.Declaration().Routes {
		if _, retired := goneSessions[r.Pattern]; retired {
			t.Errorf("retired address %s %s is published in the declaration", r.Method, r.Pattern)
		}
	}
	for _, r := range apiContract {
		if _, retired := goneSessions[r.path]; retired {
			t.Errorf("retired address %s is still a contract line", r.path)
		}
	}
}

// TestEverySuccessorIsAnAddressVisorServes is what stops the table going stale.
// A retirement that points at nothing is a 410 that helps nobody, and the
// successor is written by hand — so it is checked against the live surface,
// with an RFC 6570 template read as the route it templates.
func TestEverySuccessorIsAnAddressVisorServes(t *testing.T) {
	served := map[string]bool{}
	for k := range registeredRoutes(t) {
		_, path, _ := strings.Cut(k, " ")
		served[path] = true
	}

	for path, to := range goneSessions {
		for _, s := range to {
			route := strings.NewReplacer("{", ":", "}", "").Replace(s)
			if !served[route] {
				t.Errorf("%s names successor %s, which visor does not serve", path, s)
			}
		}
	}
}
