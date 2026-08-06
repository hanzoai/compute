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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestAuthzJudgesTheIdNotTheOwner states the premise the handlers are written
// against, and it is stated HERE because this is the only place that can state
// it: getObject is the filter's own answer to "which object is this request
// about", and controllers/agreement_test.go is written on the assumption that
// the answer comes from `?id=` and the body — never from `?owner`.
//
// Why the pair matters: a request carrying BOTH, disagreeing, is judged on one
// and used to be served on the other. Either half moving without the other
// reopens that gap, so each half is pinned where it lives — the field judged
// here, the field served there.
func TestAuthzJudgesTheIdNotTheOwner(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		owner, obj string
	}{
		{
			name:   "a GET carrying both is judged on the id",
			method: http.MethodGet, path: "/probe?owner=victim&id=attacker/anything",
			owner: "attacker", obj: "anything",
		},
		{
			name:   "a GET with no id falls back to the owner",
			method: http.MethodGet, path: "/probe?owner=victim",
			owner: "victim", obj: "",
		},
		{
			name:   "a write carrying both is judged on the body",
			method: http.MethodPost, path: "/probe?owner=victim",
			body:  `{"owner":"attacker","name":"anything"}`,
			owner: "attacker", obj: "anything",
		},
		{
			name:   "a write's id still wins over its body",
			method: http.MethodPost, path: "/probe?owner=victim&id=attacker/anything",
			body:  `{"owner":"someoneelse","name":"x"}`,
			owner: "attacker", obj: "anything",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotOwner, gotName := objectOf(t, c.method, c.path, c.body)
			if gotOwner != c.owner || gotName != c.obj {
				t.Fatalf("getObject = (%q, %q), want (%q, %q) — the field the filter judges moved, "+
					"so it may no longer be the field the handlers are written against",
					gotOwner, gotName, c.owner, c.obj)
			}
		})
	}
}

// objectOf drives one real request through a bare app and reports what the
// filter's own getObject made of it.
func objectOf(t *testing.T, method, path, body string) (owner, name string) {
	t.Helper()
	app := zip.New(zip.Config{})
	app.All("/probe", func(c *zip.Ctx) error {
		owner, name = getObject(c)
		return nil
	})

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	return owner, name
}
