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
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A provider holds an org's cloud credentials, so the question these ask is not
// whether the new addresses route — it is whether they resolve the same OBJECT
// the retired ones did. authz.IsAllowed admits a subject to its own org's
// objects (subOwner == objOwner), and getObject supplies the objOwner half. A
// move that changed where the owner is read from would change who is admitted
// without touching a policy line.

// TestProviderObjectComesFromTheAddress pins what getObject answers for each of
// the five provider addresses, against what the retired address it replaced
// answered.
//
// The PUT case is the one worth reading twice: the body carries a provider with
// a different owner, and the URL still wins. That is what makes the address the
// authority — before the move the same request was POST ?id=…, where the query
// beat the body for the same reason.
func TestProviderObjectComesFromTheAddress(t *testing.T) {
	app := zip.New(zip.Config{})

	var gotOwner, gotName string
	seen := func(c *zip.Ctx) error {
		gotOwner, gotName = getObject(c)
		return c.JSON(http.StatusOK, struct{}{})
	}
	app.Get("/v1/providers", seen)
	app.Post("/v1/providers", seen)
	app.Get("/v1/providers/:owner/:name", seen)
	app.Put("/v1/providers/:owner/:name", seen)
	app.Delete("/v1/providers/:owner/:name", seen)
	// The control lives in the same app: an opaque `:id` elsewhere on the surface
	// is a cloud's own machine identifier, not an org, and must not be read as one.
	app.Get("/v1/machines/:id", seen)

	cases := []struct {
		what        string
		method      string
		target      string
		body        string
		owner, name string
	}{
		{"list is filtered by ?owner, as it always was",
			http.MethodGet, "/v1/providers?owner=acme", "", "acme", ""},
		{"create names the object in its body, as it always did",
			http.MethodPost, "/v1/providers", `{"owner":"acme","name":"do"}`, "acme", "do"},
		{"read takes both halves from the path",
			http.MethodGet, "/v1/providers/acme/do", "", "acme", "do"},
		{"replace takes them from the path even when the body disagrees",
			http.MethodPut, "/v1/providers/acme/do", `{"owner":"evil","name":"theirs"}`, "acme", "do"},
		{"remove takes them from the path and reads no body",
			http.MethodDelete, "/v1/providers/acme/do", "", "acme", "do"},
		{"an opaque machine id is not an org",
			http.MethodGet, "/v1/machines/i-42", "", "", ""},
	}

	for _, c := range cases {
		gotOwner, gotName = "", ""
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		if c.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.target, err)
		}
		res.Body.Close()
		if gotOwner != c.owner || gotName != c.name {
			t.Errorf("%s: %s %s resolved (%q, %q), want (%q, %q)",
				c.what, c.method, c.target, gotOwner, gotName, c.owner, c.name)
		}
	}
}

// TestProviderOpsReachTheDocument is why these five are TYPED rather than
// handlers behind h(): an op is the ONE value every projection is built from, so
// reaching the OpenAPI document is the same fact as having an MCP tool, a CLI
// command, an SDK method and a by-name call. An untyped route is on the wire and
// in none of them.
//
// The same assertion carries the other half end to end: the retired addresses
// SERVE and appear nowhere in the document, which is what zip.Undeclared buys and
// what a Declares check alone cannot show.
func TestProviderOpsReachTheDocument(t *testing.T) {
	doc, err := json.Marshal(surface().OpenAPISpec())
	if err != nil {
		t.Fatalf("openapi: %v", err)
	}
	published := string(doc)

	for _, want := range []string{
		"/v1/providers", "/v1/providers/{owner}/{name}",
		"listProviders", "addProvider", "getProvider", "replaceProvider", "removeProvider",
	} {
		if !strings.Contains(published, want) {
			t.Errorf("the document does not mention %s", want)
		}
	}
	for retired := range goneProviders {
		if strings.Contains(published, retired) {
			t.Errorf("the document publishes the retired address %s", retired)
		}
	}
}

// TestProviderRetirementNamesTheSuccessor pins that every address the providers
// family retired has a row saying where it went, and that no row names an
// address that is no longer served. A retirement with no successor is a 410 that
// tells a caller nothing it could not have guessed from a 404.
func TestProviderRetirementNamesTheSuccessor(t *testing.T) {
	want := map[string]string{
		"/v1/get-providers":   "/v1/providers",
		"/v1/add-provider":    "/v1/providers",
		"/v1/get-provider":    "/v1/providers/{owner}/{name}",
		"/v1/update-provider": "/v1/providers/{owner}/{name}",
		"/v1/delete-provider": "/v1/providers/{owner}/{name}",
	}
	if len(goneProviders) != len(want) {
		t.Fatalf("retirement table has %d rows, want %d", len(goneProviders), len(want))
	}
	for path, to := range want {
		got, ok := goneProviders[path]
		if !ok {
			t.Errorf("%s is not retired", path)
			continue
		}
		if len(got) != 1 || got[0] != to {
			t.Errorf("%s -> %v, want [%s]", path, got, to)
		}
	}
}
