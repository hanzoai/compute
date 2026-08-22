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
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// TestWhitelabelVerbIsGone pins the retirement of /v1/get-whitelabel, which the
// console called on every page load before a visitor had signed in.
func TestWhitelabelVerbIsGone(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	for from, to := range goneWhitelabel {
		answersGone(t, app, http.MethodGet, from, to)
		answersGone(t, app, http.MethodPost, from, to)
		if app.Declares(http.MethodGet, from) {
			t.Errorf("GET %s is retired but still published in the declaration", from)
		}
	}
}

// TestWhitelabelAnswersItsHost proves the op reads the hostname it was ASKED
// about, and answers with the branding itself.
//
// Two things at once, and both are the migration: the Host is a DECLARED input
// (header:"Host") rather than something the handler reaches out of the request,
// which is what lets an in-process caller ask about a host this process is not
// serving; and the answer is the value, not {status,msg,data} wrapped around it.
func TestWhitelabelAnswersItsHost(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	if !app.Declares(http.MethodGet, "/v1/whitelabel") {
		t.Fatal("GET /v1/whitelabel is not published — the contract cannot be read")
	}

	req := httptest.NewRequest(http.MethodGet, "http://visor.lux.network/v1/whitelabel", nil)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET /v1/whitelabel: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/whitelabel = %d, want 200", res.StatusCode)
	}
	var got object.WhitelabelConfig
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("body: %v", err)
	}
	// The Hanzo default would answer here if the Host never reached the handler,
	// so naming the Lux brand is what proves the binding rather than the fallback.
	if want := "Lux Visor"; got.AppName != want {
		t.Errorf("appName = %q, want %q — the Host header did not reach the op", got.AppName, want)
	}
}
