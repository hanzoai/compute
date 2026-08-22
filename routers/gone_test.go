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
)

// TestGoneAnswersItsSuccessor drives a retired address and reads what a caller
// still sending it gets back.
//
// The point of a retirement is the FORWARDING. A 404 would be honest about the
// address and useless about the move, so what is asserted is the pair: the status
// that says the address is gone, and the successor that says where it went — in
// the Link header a machine follows and in the body a human reads, from the same
// row so the two cannot disagree.
func TestGoneAnswersItsSuccessor(t *testing.T) {
	app := surface()

	for path, want := range goneProviders {
		res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if res.StatusCode != http.StatusGone {
			t.Errorf("GET %s = %d, want 410", path, res.StatusCode)
			continue
		}
		for _, to := range want {
			if link := res.Header.Get("Link"); !strings.Contains(link, "<"+to+`>; rel="successor-version"`) {
				t.Errorf("GET %s: Link = %q, want it to name %s", path, link, to)
			}
		}
		if d := res.Header.Get("Deprecation"); !strings.HasPrefix(d, "@") {
			t.Errorf("GET %s: Deprecation = %q, want a structured date (@<unix>)", path, d)
		}
		if res.Header.Get("Sunset") == "" {
			t.Errorf("GET %s: no Sunset", path)
		}

		var got notice
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("GET %s: body %q: %v", path, body, err)
			continue
		}
		if strings.Join(got.Successor, ",") != strings.Join(want, ",") {
			t.Errorf("GET %s: body successor = %v, want %v", path, got.Successor, want)
		}
	}
}

// TestGoneAnswersEveryMethod pins the reason these are registered with All. 410
// is a statement about the target RESOURCE, so the address is gone whatever verb
// reaches it — and a caller that sent the wrong one would otherwise get a 405
// and no successor, which is the case least able to find the new address on its
// own.
func TestGoneAnswersEveryMethod(t *testing.T) {
	app := surface()

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		res, err := app.Fiber().Test(httptest.NewRequest(method, "/v1/get-provider", nil))
		if err != nil {
			t.Fatalf("%s /v1/get-provider: %v", method, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusGone {
			t.Errorf("%s /v1/get-provider = %d, want 410", method, res.StatusCode)
		}
	}
}

// TestGoneIsUndeclared is the other half, and the reason zip.Undeclared exists:
// a retired address SERVES and is not part of the contract. Since it answers
// every method, publishing it would put one dead operation per method per
// address into the OpenAPI document, the MCP tool list, the CLI and the SDKs.
//
// It asserts the served/declared split directly, because the contract test
// filters on exactly this and so cannot notice if the split stopped working.
func TestGoneIsUndeclared(t *testing.T) {
	app := surface()

	for path := range goneProviders {
		if app.Declares(http.MethodGet, path) {
			t.Errorf("%s is in the declaration — a retired address must serve without being published", path)
		}
	}
	// The control: the successor IS declared. Without it this passes just as well
	// when nothing at all is declared.
	if !app.Declares(http.MethodGet, "/v1/providers") {
		t.Error("/v1/providers is not declared — the successor must be in the contract")
	}
}
