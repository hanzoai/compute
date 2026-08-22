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

// These pin the WIRE of the volume ops: what a caller receives, which no
// compiler checks. The old wire was casibase's — HTTP 200 always, with
// {status,msg,data} around the value and a logical failure sitting inside a
// success. A typed op has no envelope: the answer IS the value and the status IS
// the outcome.
//
// The store here is the Base backend at a temp dataRoot (TestMain), so what can
// be exercised is the paths that do not consult a cloud — the list, the reads
// and the refusals. A create, attach, detach or resize reaches the provider, so
// it has no honest answer without one and is not faked into having one.
//
// Addresses are pinned separately, by routers/router_contract_test.go.

// send drives one real request of any method and returns the status and the raw
// body. The volume surface answers on five verbs, which is the point of it.
func send(t *testing.T, app *zip.App, method, path, bearer, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
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

// volumeWire stands the seven ops up on a bare app, with no filter chain,
// because the question here is what a HANDLER answers.
func volumeWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/volumes", ListVolumes)
	zip.Post(app, "/v1/volumes", CreateVolume)
	zip.Get(app, "/v1/volumes/:id", GetVolume)
	zip.Patch(app, "/v1/volumes/:id", ResizeVolume)
	zip.Delete(app, "/v1/volumes/:id", DeleteVolume)
	zip.Put(app, "/v1/volumes/:id/attachment", AttachVolume)
	zip.Delete(app, "/v1/volumes/:id/attachment", DetachVolume)
	return app
}

// TestListIsAnObjectKeyedVolumes pins the collection shape. A bare JSON array
// would decode as readily, and that is the reason to assert the key: a
// collection that answers an array has nowhere to put a cursor or a count later
// without breaking every client.
func TestListIsAnObjectKeyedVolumes(t *testing.T) {
	app := volumeWire(t)
	mine := org(t)
	storedVolume(t, mine, "data-a")

	status, body := send(t, app, http.MethodGet, "/v1/volumes?owner="+mine, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/volumes = %d %s, want 200", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("GET /v1/volumes answered the casibase envelope: %s", body)
	}

	var got struct {
		Volumes []object.Volume `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got.Volumes) != 1 || got.Volumes[0].Name != "data-a" {
		t.Fatalf("volumes = %s, want the one row data-a", body)
	}
}

// TestEmptyListIsAnEmptyArray — an org with no volumes answers [], never null.
// A null there makes every client range over a nil and reach for a nil check the
// shape should have made unnecessary.
func TestEmptyListIsAnEmptyArray(t *testing.T) {
	app := volumeWire(t)

	status, body := send(t, app, http.MethodGet, "/v1/volumes?owner="+org(t), "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/volumes = %d %s, want 200", status, body)
	}
	if !strings.Contains(body, `"volumes":[]`) {
		t.Fatalf("an empty list answered %s, want an empty array", body)
	}
}

// TestReadOfAnAbsentVolumeIs404. The old wire said 200 with `data: null`, so
// "this org has no such volume" and "here is the volume" were the same status,
// told apart only by looking inside.
func TestReadOfAnAbsentVolumeIs404(t *testing.T) {
	app := volumeWire(t)

	status, body := send(t, app, http.MethodGet, "/v1/volumes/nothing-here?owner="+org(t), "", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET /v1/volumes/nothing-here = %d %s, want 404", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("the refusal answered the casibase envelope: %s", body)
	}
}

// TestReadIsTheVolumeItself pins that the answer is the value, with no envelope
// around it — the row's own fields at the top level.
func TestReadIsTheVolumeItself(t *testing.T) {
	app := volumeWire(t)
	mine := org(t)
	storedVolume(t, mine, "data-a")

	status, body := send(t, app, http.MethodGet, "/v1/volumes/data-a?owner="+mine, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/volumes/data-a = %d %s, want 200", status, body)
	}
	var got object.Volume
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got.Name != "data-a" || got.Owner != mine || got.Size != 100 {
		t.Fatalf("read answered %s, want the stored row", body)
	}
}

// TestResizeRefusesANonSizeBeforeItReachesACloud. The bound is declared on the
// input (min=1), so a missing size and a negative one are one rule and both are
// refused with nothing provisioned.
func TestResizeRefusesANonSizeBeforeItReachesACloud(t *testing.T) {
	app := volumeWire(t)
	mine := org(t)
	storedVolume(t, mine, "data-a")

	for name, body := range map[string]string{
		"absent":   `{}`,
		"zero":     `{"size":0}`,
		"negative": `{"size":-10}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, got := send(t, app, http.MethodPatch, "/v1/volumes/data-a?owner="+mine, "", body)
			if status != http.StatusBadRequest {
				t.Fatalf("PATCH with a %s size = %d %s, want 400", name, status, got)
			}
		})
	}
}

// TestAttachRefusesAnEmptyMachine — the attachment names what it attaches to,
// and a PUT that names nothing is not a state anything can be put into.
func TestAttachRefusesAnEmptyMachine(t *testing.T) {
	app := volumeWire(t)
	mine := org(t)
	storedVolume(t, mine, "data-a")

	status, body := send(t, app, http.MethodPut, "/v1/volumes/data-a/attachment?owner="+mine, "", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("PUT attachment with no machine = %d %s, want 400", status, body)
	}
}
