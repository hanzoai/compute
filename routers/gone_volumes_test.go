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
	"regexp"
	"testing"

	"github.com/zap-proto/zip"
)

// goneApp installs the retired volume addresses on a bare app.
func goneApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	registerGoneVolumes(app)
	return app
}

// TestRetiredVolumeAddressesAnswerGone — every method, because 410 is a
// statement about the target resource and a caller who sent the wrong verb still
// needs to be told where the resource went.
func TestRetiredVolumeAddressesAnswerGone(t *testing.T) {
	app := goneApp(t)

	for addr := range goneVolumes {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			res, err := app.Fiber().Test(httptest.NewRequest(m, addr, nil))
			if err != nil {
				t.Fatalf("%s %s: %v", m, addr, err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusGone {
				t.Errorf("%s %s = %d, want 410", m, addr, res.StatusCode)
			}
		}
	}
}

// TestRetiredVolumeAddressesNameTheirSuccessor pins the three stamps and the
// body against the SAME table row, which is the property that keeps them from
// drifting apart: a header naming one address and a body naming another is worse
// than either alone, because a client picks one and never learns of the other.
func TestRetiredVolumeAddressesNameTheirSuccessor(t *testing.T) {
	app := goneApp(t)

	for addr, to := range goneVolumes {
		res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, addr, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", addr, err)
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: read body: %v", addr, err)
		}

		if got, want := res.Header.Get("Link"), "<"+to.Path+`>; rel="successor-version"`; got != want {
			t.Errorf("%s Link = %q, want %q", addr, got, want)
		}
		// RFC 9745 — a structured Date, which serialises as "@" and a Unix
		// second. RFC 8594 — an HTTP-date. Both stamp NOW: the address is gone,
		// not going, and a Sunset a caller reads as "still time" is a lie once
		// the address answers 410.
		if d := res.Header.Get("Deprecation"); len(d) < 2 || d[0] != '@' {
			t.Errorf("%s Deprecation = %q, want a structured Date", addr, d)
		}
		if s := res.Header.Get("Sunset"); s == "" {
			t.Errorf("%s served no Sunset", addr)
		} else if _, err := http.ParseTime(s); err != nil {
			t.Errorf("%s Sunset = %q, not an HTTP-date: %v", addr, s, err)
		}

		var doc struct {
			Status    int `json:"status"`
			Successor struct {
				Method string `json:"method"`
				Path   string `json:"path"`
			} `json:"successor"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: decode %s: %v", addr, body, err)
		}
		if doc.Status != http.StatusGone {
			t.Errorf("%s problem document status = %d, want 410", addr, doc.Status)
		}
		if doc.Successor.Method != to.Method || doc.Successor.Path != to.Path {
			t.Errorf("%s body names %s %s, want %s %s", addr,
				doc.Successor.Method, doc.Successor.Path, to.Method, to.Path)
		}
	}
}

// TestRetiredVolumeAddressesAreNotDeclared is the other half, and it is the
// reason they are registered on zip.Undeclared: a retirement that reached the
// declaration would publish one operation per method per address into the
// OpenAPI document, the MCP tool list, the CLI and the SDKs — a customer
// contract listing dead endpoints, which is a contract nobody can read.
func TestRetiredVolumeAddressesAreNotDeclared(t *testing.T) {
	app := zip.New(zip.Config{})
	registerVolume(app)
	registerGoneVolumes(app)

	for _, r := range app.Declaration().Routes {
		if _, retired := goneVolumes[r.Pattern]; retired {
			t.Errorf("%s %s reached the declaration, and so every projection built from it", r.Method, r.Pattern)
		}
	}
	// The living noun beside them is declared, so the check above is not passing
	// on an empty declaration.
	if !declares(app, http.MethodGet, "/v1/volumes") {
		t.Fatal("GET /v1/volumes is missing from the declaration — the test proves nothing")
	}
}

// TestEveryRetiredVolumeAddressNamesALivingSuccessor closes the loop the other
// way: a table row is only useful if the address it points at is one visor
// actually serves, and a rename that moves the successor again would otherwise
// leave the tombstone pointing into nothing.
func TestEveryRetiredVolumeAddressNamesALivingSuccessor(t *testing.T) {
	app := zip.New(zip.Config{})
	registerVolume(app)

	for addr, to := range goneVolumes {
		if !declares(app, to.Method, to.Path) {
			t.Errorf("%s names successor %s %s, which visor does not serve", addr, to.Method, to.Path)
		}
	}
}

// declares reports whether the app publishes method+path — whether it is in
// App.Declaration, and so in every projection built from it.
//
// It takes the address as a URI template, which is how the retirement table
// writes it and how a client and an OpenAPI document read it, and matches it
// against the router's own spelling: {id} there is :id here, and nothing else
// differs between the two.
func declares(app *zip.App, method, path string) bool {
	want := regexp.MustCompile(`\{([^}]*)\}`).ReplaceAllString(path, ":$1")
	for _, r := range app.Declaration().Routes {
		if r.Method == method && r.Pattern == want {
			return true
		}
	}
	return false
}
