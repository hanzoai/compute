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
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// These tests pin the WIRE of the session ops: what each address ANSWERS, which
// no compiler checks. The store is the Base backend the package's TestMain
// installs, so every one of them runs against real SQLite rather than a fake.
//
// Addresses are pinned separately, by routers/router_contract_test.go. What is
// registered here is the same set of ops at the same paths, on a bare app with
// no filter chain, because the question is what a HANDLER answers.

// sessions stands the six ops up on a bare app.
func sessions(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/sessions", ListSessions)
	zip.Get(app, "/v1/sessions/:owner/:name", GetSession)
	zip.Put(app, "/v1/sessions/:owner/:name", ReplaceSession)
	zip.Delete(app, "/v1/sessions/:owner/:name", DeleteSession)
	zip.Put(app, "/v1/sessions/:owner/:name/connection", ConnectSession)
	zip.Delete(app, "/v1/sessions/:owner/:name/connection", DisconnectSession)
	return app
}

// send drives one real request, with a body when there is one.
func send(t *testing.T, app *zip.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := app.Fiber().Test(req)
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

// seed writes one session of the test's own org and returns its name.
func seed(t *testing.T, owner, name, status string) string {
	t.Helper()
	if _, err := object.AddSession(&object.Session{
		Owner: owner, Name: name, Status: status, Protocol: "SSH", Asset: owner + "/drop-a",
	}); err != nil {
		t.Fatalf("seed session %s/%s: %v", owner, name, err)
	}
	return name
}

// TestConnectionDeleteLeavesTheRecord is the whole reason the live connection is
// a SUB-RESOURCE and not a value of the status column: the two DELETEs remove
// two different things. This one closes the tunnel and leaves the record; the
// one below removes the record itself.
func TestConnectionDeleteLeavesTheRecord(t *testing.T) {
	app := sessions(t)
	mine := org(t)
	name := seed(t, mine, "s-1", object.Connected)

	status, body := send(t, app, http.MethodDelete, "/v1/sessions/"+mine+"/"+name+"/connection", "")
	if status != http.StatusNoContent {
		t.Fatalf("DELETE .../connection = %d %s, want 204", status, body)
	}
	if body != "" {
		t.Fatalf("DELETE .../connection answered a body %q, want none", body)
	}

	kept, err := object.GetConnSession(mine + "/" + name)
	if err != nil || kept == nil {
		t.Fatalf("the record went with the connection: %v %v", kept, err)
	}
	if kept.Status != object.Disconnected {
		t.Fatalf("status = %q after the connection was closed, want %q", kept.Status, object.Disconnected)
	}
}

// TestSessionDeleteRemovesTheRecord is the other half of the pair above.
func TestSessionDeleteRemovesTheRecord(t *testing.T) {
	app := sessions(t)
	mine := org(t)
	name := seed(t, mine, "s-1", object.Connected)

	status, body := send(t, app, http.MethodDelete, "/v1/sessions/"+mine+"/"+name, "")
	if status != http.StatusNoContent {
		t.Fatalf("DELETE /v1/sessions/%s/%s = %d %s, want 204", mine, name, status, body)
	}

	gone, err := object.GetConnSession(mine + "/" + name)
	if err != nil {
		t.Fatalf("read after delete: %v", err)
	}
	if gone != nil {
		t.Fatalf("the record survived its own DELETE: %+v", gone)
	}
}

// TestConnectStampsTheStart pins what the guacamole client's report does: the
// session becomes connected and gets a start time. It is a PUT because sending
// it twice is the state asked for, not two connections.
func TestConnectStampsTheStart(t *testing.T) {
	app := sessions(t)
	mine := org(t)
	name := seed(t, mine, "s-1", object.NoConnect)

	status, body := send(t, app, http.MethodPut, "/v1/sessions/"+mine+"/"+name+"/connection", "")
	if status != http.StatusOK {
		t.Fatalf("PUT .../connection = %d %s, want 200", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("PUT .../connection answered the casibase envelope: %s", body)
	}

	var got object.Session
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got.Status != object.Connected {
		t.Fatalf("status = %q, want %q", got.Status, object.Connected)
	}
	if got.StartTime == "" {
		t.Fatalf("startTime is empty in %s", body)
	}
}

// TestReadOfAnAbsentSessionIs404 — absent is a status, not a 200 carrying null.
func TestReadOfAnAbsentSessionIs404(t *testing.T) {
	app := sessions(t)

	status, body := send(t, app, http.MethodGet, "/v1/sessions/"+org(t)+"/nobody", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET an absent session = %d %s, want 404", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("GET answered the casibase envelope: %s", body)
	}
}

// TestListIsKeyedSessionsWithACount pins the collection shape. The count is the
// whole filtered set rather than the page, which is what a caller paging through
// needs and what the two `data`/`data2` fields of the envelope used to carry.
func TestListIsKeyedSessionsWithACount(t *testing.T) {
	app := sessions(t)
	mine := org(t)
	seed(t, mine, "s-1", object.Connected)
	seed(t, mine, "s-2", object.Disconnected)

	status, body := send(t, app, http.MethodGet, "/v1/sessions?owner="+mine, "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/sessions = %d %s, want 200", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("GET /v1/sessions answered the casibase envelope: %s", body)
	}

	var got Sessions
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got.Sessions) != 2 || got.Count != 2 {
		t.Fatalf("got %d rows / count %d in %s, want 2 / 2", len(got.Sessions), got.Count, body)
	}
}

// TestListFiltersOnEveryRead is the branch the two old ones lost: an unpaged
// read used to ignore status, field, value and the sort, so the same query
// answered differently depending on whether a page size came with it.
func TestListFiltersOnEveryRead(t *testing.T) {
	app := sessions(t)
	mine := org(t)
	seed(t, mine, "s-1", object.Connected)
	seed(t, mine, "s-2", object.Disconnected)

	_, body := send(t, app, http.MethodGet, "/v1/sessions?owner="+mine+"&status="+object.Connected, "")
	var got Sessions
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Status != object.Connected {
		t.Fatalf("unpaged read ignored ?status: %s", body)
	}
}

// TestListRefusesAnUnresolvableScope is the closed door. With neither a Bearer
// nor an ?owner there is no tenant, and under the Base backend the empty org
// resolves to the shared `_global` database — which holds no tenant's sessions,
// so the read used to answer 200 with an empty page. An unresolvable scope is an
// error; a silent empty is a lie about the tenant's data.
func TestListRefusesAnUnresolvableScope(t *testing.T) {
	app := sessions(t)

	status, body := send(t, app, http.MethodGet, "/v1/sessions", "")
	if status != http.StatusBadRequest {
		t.Fatalf("GET /v1/sessions with no scope = %d %s, want 400", status, body)
	}
}

// TestTheUrlNamesTheSession pins the addressing authority: the path wins over
// the body, so a replace cannot smuggle a different owner or name past the
// address it was sent to.
func TestTheUrlNamesTheSession(t *testing.T) {
	app := sessions(t)
	mine, theirs := org(t), org(t)+"-other"
	name := seed(t, mine, "s-1", object.Connected)
	seed(t, theirs, "s-1", object.Connected)

	body := `{"owner":"` + theirs + `","name":"s-1","status":"` + object.Disconnected + `","protocol":"RDP"}`
	status, got := send(t, app, http.MethodPut, "/v1/sessions/"+mine+"/"+name, body)
	if status != http.StatusOK {
		t.Fatalf("PUT /v1/sessions/%s/%s = %d %s, want 200", mine, name, status, got)
	}

	victim, err := object.GetConnSession(theirs + "/s-1")
	if err != nil {
		t.Fatalf("read %s: %v", theirs, err)
	}
	if victim.Protocol != "SSH" {
		t.Fatalf("the body's owner reached %s: protocol = %q, want SSH", theirs, victim.Protocol)
	}
	mineNow, err := object.GetConnSession(mine + "/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", mine, err)
	}
	if mineNow.Protocol != "RDP" {
		t.Fatalf("the addressed session was not replaced: protocol = %q, want RDP", mineNow.Protocol)
	}
}
