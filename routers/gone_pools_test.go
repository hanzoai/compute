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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// goneApp stands the retired node-pool addresses up on a bare app, with no
// filter chain — the question here is what the ADDRESS answers.
func goneApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	routeGonePools(app)
	return app
}

// TestRetiredPoolAddressIsGoneOnEveryMethod drives every retired address with
// methods it used to answer and methods it never did. 410 is a statement about
// the target resource, so a caller that reaches for the wrong verb still gets
// told where the thing went — a 405 would leave it with nothing to follow.
func TestRetiredPoolAddressIsGoneOnEveryMethod(t *testing.T) {
	app := goneApp(t)

	for path := range GonePools {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			res, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusGone {
				t.Errorf("%s %s = %d %s, want 410", method, path, res.StatusCode, body)
			}
		}
	}
}

// TestRetiredPoolAddressNamesItsSuccessorTwice pins the whole notice: the Link
// header (RFC 5829), the Deprecation and Sunset stamps (RFC 9745, RFC 8594), and
// the body — which must name the SAME successor the header does, because they
// are rendered from one row and a caller may read either.
func TestRetiredPoolAddressNamesItsSuccessorTwice(t *testing.T) {
	app := goneApp(t)

	for path, want := range GonePools {
		res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		link := res.Header.Get("Link")
		for _, to := range want {
			if !strings.Contains(link, "<"+to+`>; rel="successor-version"`) {
				t.Errorf("GET %s Link = %q, want a successor-version link to %s", path, link, to)
			}
		}

		var notice struct {
			Successor []string `json:"successor"`
		}
		if err := json.Unmarshal(body, &notice); err != nil {
			t.Fatalf("GET %s body %q: %v", path, body, err)
		}
		if strings.Join(notice.Successor, ",") != strings.Join(want, ",") {
			t.Errorf("GET %s body successor = %v, want %v", path, notice.Successor, want)
		}

		// Both stamps are now, and Sunset may not precede Deprecation (RFC 9745,
		// section 4). "Now" is the honest reading of an address that is already
		// unresponsive, not a placeholder date somebody has to keep true.
		dep := res.Header.Get("Deprecation")
		secs, err := strconv.ParseInt(strings.TrimPrefix(dep, "@"), 10, 64)
		if !strings.HasPrefix(dep, "@") || err != nil {
			t.Errorf("GET %s Deprecation = %q, want @<unix seconds>", path, dep)
			continue
		}
		sunset, err := time.Parse(http.TimeFormat, res.Header.Get("Sunset"))
		if err != nil {
			t.Errorf("GET %s Sunset = %q: %v", path, res.Header.Get("Sunset"), err)
			continue
		}
		if sunset.Unix() < secs {
			t.Errorf("GET %s Sunset %v precedes Deprecation %v", path, sunset.Unix(), secs)
		}
	}
}

// TestRetiredPoolAddressAnswersUnauthenticated is the point of registering these
// AHEAD of the filter chain, stated as a request — it goes through Route, so the
// static filter, the tenant filter, the authorizer and the audit recorder are all
// installed.
//
// A caller holding a dead address has no credential that would help it, and
// behind ApiFilter the 410 that names the successor becomes a 403 that names
// nothing. Then the one thing the caller needed — where the thing went — is the
// one thing it cannot learn.
func TestRetiredPoolAddressAnswersUnauthenticated(t *testing.T) {
	for path, want := range GonePools {
		status, body := get(t, path)
		if status != http.StatusGone {
			t.Errorf("GET %s with no credentials = %d %s, want 410", path, status, body)
		}
		for _, to := range want {
			if !strings.Contains(body, to) {
				t.Errorf("GET %s answered %s, which does not name its successor %s", path, body, to)
			}
		}
	}
}

// TestRetiredPoolAddressIsNotTheContract is why these are registered on
// zip.Undeclared. Each answers every method, so publishing them would be one
// operation per method per address — dozens of calls that never existed, in the
// document a customer reads. They SERVE and they are not the contract.
func TestRetiredPoolAddressIsNotTheContract(t *testing.T) {
	app := zip.New(zip.Config{})
	routeGonePools(app)
	registerAPI(app)

	for _, r := range app.Declaration().Routes {
		if _, retired := GonePools[r.Pattern]; retired {
			t.Errorf("retired address %s %s is published in the declaration", r.Method, r.Pattern)
		}
	}
}

// TestRetiredPoolSuccessorIsServed keeps the notice honest: every address it
// points a caller at is one this service actually answers. A retirement that
// names a path nobody serves is a 410 followed by a 404.
func TestRetiredPoolSuccessorIsServed(t *testing.T) {
	served := registeredRoutes(t)

	paths := map[string]bool{}
	for k := range served {
		_, path, _ := strings.Cut(k, " ")
		paths[path] = true
	}
	for from, to := range GonePools {
		for _, s := range to {
			if !paths[s] {
				t.Errorf("%s names successor %s, which nothing serves", from, s)
			}
		}
	}
}
