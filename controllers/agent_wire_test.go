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

package controllers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// These tests pin the WIRE of the agent ops, which is the half of this change
// that no compiler checks and that a caller in another repo depends on byte for
// byte. hanzoai/cloud's apps/visor/client.go reads these four answers; every
// assertion below is a line in that client, and the fake vm in its
// bots_http_test.go is written to match exactly what is asserted here.
//
// The old wire was casibase's: HTTP 200 always, with {status,msg,data} around
// the value and a logical failure sitting inside a success. A typed op has no
// envelope — the answer IS the value and the status IS the outcome — so these
// assertions are about the SHAPE as much as the content.
//
// Addresses are pinned separately, by routers/router_contract_test.go. What is
// registered here is the same set of ops at the same paths, on a bare app with
// no filter chain, because the question is what a handler ANSWERS.

// TestMain installs the store ONCE for the whole binary, and the "once" is
// load-bearing: `object`'s store is process-wide, so a per-test one would leave
// the previous test's engines open over a dataRoot that its own t.TempDir
// cleanup had already deleted — which reads as a SQLite "disk I/O error" in a
// LATER test and disappears the moment either test is run alone.
//
// The store is the Base backend at a temp dataRoot: no Postgres, no provider
// credential, nothing to reach. That bounds what can be tested here to the paths
// that do not consult the cloud — which is exactly the three answers whose shape
// changed (absent read, unbind, list). A bind resolves the machine at
// DigitalOcean, so it has no honest answer without a provider and is not faked
// into one.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "visor-agent-wire")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("dataRoot", root)
	object.InitAdapter()
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// org is a test's OWN tenant, named after it. Under the Base backend each org is
// a separate SQLite file, so this is real isolation from one shared store rather
// than a convention two tests could violate — and it is the same property the
// production backend rests on, exercised rather than assumed.
func org(t *testing.T) string {
	t.Helper()
	return strings.ToLower(t.Name())
}

// wire stands the four ops up on a bare app.
func wire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/machines/agents", ListAgents)
	zip.Put(app, "/v1/machines/:id/agent", BindAgent)
	zip.Get(app, "/v1/machines/:id/agent", GetAgent)
	zip.Delete(app, "/v1/machines/:id/agent", UnbindAgent)
	return app
}

// ask drives one real request and returns the status and the raw body.
func ask(t *testing.T, app *zip.App, method, path string) (int, string) {
	t.Helper()
	res, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res.StatusCode, string(b)
}

// TestReadOfAnAbsentBindingIs404 is the answer cloud's getAgent turns into its
// own 404, and the reason it can stop inspecting fields to find out.
//
// The old wire said 200 with `data: null`, so "this machine runs no agent" and
// "here is the agent" were the same status and told apart only by looking inside
// — which is how a caller comes to treat a zero-valued binding as a real one.
func TestReadOfAnAbsentBindingIs404(t *testing.T) {
	app := wire(t)

	status, body := ask(t, app, http.MethodGet, "/v1/machines/drop-a/agent?owner="+org(t))

	if status != http.StatusNotFound {
		t.Fatalf("GET .../agent (no binding) = %d %s, want 404", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("GET .../agent answered the casibase envelope: %s", body)
	}
}

// TestUnbindIs204 pins the void answer. The old wire put the word "Affected" or
// "Unaffected" in a 200 body, which made every caller parse prose to learn a
// distinction none of them acted on; a DELETE that is idempotent has nothing to
// say, and 204 is how that is said.
func TestUnbindIs204(t *testing.T) {
	app := wire(t)

	status, body := ask(t, app, http.MethodDelete, "/v1/machines/drop-a/agent?owner="+org(t))

	if status != http.StatusNoContent {
		t.Fatalf("DELETE .../agent = %d %s, want 204", status, body)
	}
	if body != "" {
		t.Fatalf("DELETE .../agent answered a body %q, want none", body)
	}
}

// TestListIsAnObjectKeyedAgentBindings pins the collection shape cloud decodes
// straight into its own bindingList — one shape, both sides, no translation.
//
// A bare JSON array would decode there too, and that is the point of asserting
// the key: a collection that answers an array has nowhere to put a cursor or a
// count later without breaking every client, and cloud already publishes the
// object form.
func TestListIsAnObjectKeyedAgentBindings(t *testing.T) {
	app := wire(t)

	mine := org(t)
	if _, err := object.AddAgentBinding(&object.AgentBinding{
		Owner: mine, Name: "drop-a", MachineId: mine + "/drop-a",
		Org: mine, AgentName: "bot-a", Status: object.AgentBindingPending,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	status, body := ask(t, app, http.MethodGet, "/v1/machines/agents?owner="+mine)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/machines/agents = %d %s, want 200", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("GET /v1/machines/agents answered the casibase envelope: %s", body)
	}

	var got struct {
		AgentBindings []object.AgentBinding `json:"agentBindings"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got.AgentBindings) != 1 {
		t.Fatalf("agentBindings = %d rows in %s, want 1", len(got.AgentBindings), body)
	}
	if got.AgentBindings[0].AgentName != "bot-a" {
		t.Fatalf("agentBindings[0].agentName = %q, want bot-a", got.AgentBindings[0].AgentName)
	}
}

// TestListIsScopedToTheCallerOrg is the tenancy control. The list has no id to
// address, so its whole scope is the org `principal` resolved, and a second
// org's rows must never appear in it.
//
// Under the Base backend the two orgs are two SQLite files, so what this pins is
// that the list reads the CALLER's org and not some other one — a read that had
// lost the caller's org would resolve a different engine and come back empty,
// which is what it asserts against. The WHERE-clause form of the same property
// is what the Postgres backend would exercise; both are `GetAgentBindings(owner)`
// being handed the right owner.
func TestListIsScopedToTheCallerOrg(t *testing.T) {
	app := wire(t)

	mine, theirs := org(t), org(t)+"-other"
	for _, b := range []*object.AgentBinding{
		{Owner: mine, Name: "drop-a", Org: mine, AgentName: "bot-a"},
		{Owner: theirs, Name: "drop-b", Org: theirs, AgentName: "bot-b"},
	} {
		if _, err := object.AddAgentBinding(b); err != nil {
			t.Fatalf("seed %s: %v", b.Owner, err)
		}
	}

	_, body := ask(t, app, http.MethodGet, "/v1/machines/agents?owner="+mine)
	var got struct {
		AgentBindings []object.AgentBinding `json:"agentBindings"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	for _, b := range got.AgentBindings {
		if b.Owner != mine {
			t.Fatalf("%s's list carried %s's binding: %s", mine, b.Owner, body)
		}
	}
	if len(got.AgentBindings) != 1 {
		t.Fatalf("%s sees %d bindings in %s, want 1", mine, len(got.AgentBindings), body)
	}
}

// TestNoOrgContextIsRefused pins the refusal: with neither a Bearer nor an
// ?owner there is no tenant, and the ops must refuse rather than read whatever
// the empty org happens to hold.
func TestNoOrgContextIsRefused(t *testing.T) {
	app := wire(t)

	if status, body := ask(t, app, http.MethodGet, "/v1/machines/agents"); status != http.StatusForbidden {
		t.Errorf("GET /v1/machines/agents anonymous = %d %s, want 403", status, body)
	}
	if status, body := ask(t, app, http.MethodGet, "/v1/machines/drop-a/agent"); status != http.StatusBadRequest {
		t.Errorf("GET .../agent anonymous = %d %s, want 400", status, body)
	}
}

// envelope reports whether a body is casibase's {status,msg,data} wrapper rather
// than the value itself. It is the ONE thing every assertion above shares, and
// the reason it is a predicate rather than a string match: `status` alone is not
// evidence (an AgentBinding has its own `status` field, which is exactly why a
// naive check would pass on a real answer), so the envelope is recognised by the
// pair of keys only it carries.
func envelope(t *testing.T, body string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return false
	}
	_, hasMsg := m["msg"]
	_, hasData := m["data"]
	return hasMsg && hasData
}
