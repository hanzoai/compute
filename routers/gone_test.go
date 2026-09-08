// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A RETIRED ADDRESS ANSWERS 410 AND NAMES ITS SUCCESSOR — unauthenticated, on
// every method.
//
// Unauthenticated is the point. These addresses are registered ahead of
// ApiFilter, because behind it a caller learns about a retired address from the
// POLICY rather than from the resource: measured on an earlier attempt, one
// retired address answered 410 on GET and 403 on the other five, purely because
// a Casbin row happened to carry GET.
func TestRetiredAddressesAnswerGone(t *testing.T) {
	app := zip.New(zip.Config{AppName: "gone", DisableStartupMessage: true})
	Route(app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if len(successor) == 0 {
		t.Fatal("no retirements registered — this test would pass over an empty table")
	}
	for path, to := range successor {
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			res, err := app.Fiber().Test(httptest.NewRequest(m, path, nil))
			if err != nil {
				t.Fatalf("%s %s: %v", m, path, err)
			}
			_ = res.Body.Close()
			if res.StatusCode != 410 {
				t.Errorf("%s %s = %d, want 410 — a retired address is refused by the filter, "+
					"not answered by the retirement", m, path, res.StatusCode)
			}
			if link := res.Header.Get("Link"); !strings.Contains(link, `rel="successor-version"`) ||
				!strings.Contains(link, to) {
				t.Errorf("%s %s Link = %q, want one naming %s", m, path, link, to)
			}
			if res.Header.Get("Deprecation") == "" || res.Header.Get("Sunset") == "" {
				t.Errorf("%s %s: missing Deprecation or Sunset", m, path)
			}
		}
	}
}

// A RETIRED ADDRESS IS IN NO PROJECTION, AND ITS SUCCESSOR IS.
//
// The successor's presence is the control. Without it this passes over a
// declaration that lists nothing, which is what a broken Route() produces.
func TestRetiredAddressesAreUndeclared(t *testing.T) {
	app := zip.New(zip.Config{AppName: "gone", DisableStartupMessage: true})
	Route(app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, r := range app.Declaration().Routes {
		declared[r.Pattern] = true
	}
	for path, to := range successor {
		if declared[path] {
			t.Errorf("%s is retired but declared — it would reach the document, the MCP tool "+
				"list, the CLI and every SDK", path)
		}
		if !declared[to] {
			t.Errorf("successor %s is not declared; the absence above proves nothing", to)
		}
	}
}
