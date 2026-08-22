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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// The node pools were fixed and the same split-brain was left standing on the
// machine and volume handlers. These prove it is gone from those too.
//
// The split-brain: the authorization filter derives the object's owner from
// `?id=` or the request BODY, while these handlers read `?owner=`. Send both,
// disagreeing, and the filter authorizes against the one you own while the
// handler provisions against the one you do not — on the victim's provider
// credentials, and onto the victim's invoice.

// tenantWire stands the machine and volume handlers up on a bare app, registered
// exactly as routers.Route registers them.
func tenantWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	handler := func(fn func(*ApiController)) zip.Handler {
		return func(c *zip.Ctx) error { fn(New(c)); return nil }
	}
	app.Post("/v1/launch-machine", handler((*ApiController).LaunchMachine))
	// The volume ops are TYPED, so they are registered as ops rather than
	// through the controller bridge. What they read is the same two inputs the
	// bridge fed the old handlers — the forwarded Bearer and ?owner — declared
	// on the input type instead of fetched from the request behind the
	// document's back, which is the whole point of the conversion.
	zip.Get(app, "/v1/volumes", ListVolumes)
	zip.Post(app, "/v1/volumes", CreateVolume)
	zip.Get(app, "/v1/volumes/:id", GetVolume)
	zip.Patch(app, "/v1/volumes/:id", ResizeVolume)
	zip.Delete(app, "/v1/volumes/:id", DeleteVolume)
	zip.Put(app, "/v1/volumes/:id/attachment", AttachVolume)
	zip.Delete(app, "/v1/volumes/:id/attachment", DetachVolume)
	return app
}

// storedVolume plants a DB-only volume belonging to owner.
func storedVolume(t *testing.T, owner, name string) *object.Volume {
	t.Helper()
	now := time.Now().Format(time.RFC3339)
	v := &object.Volume{
		Owner: owner, Name: name, Id: "vol-" + name, Provider: "do",
		Region: "nyc3", Size: 100, State: "Available",
		CreatedTime: now, UpdatedTime: now,
	}
	if _, err := object.AddVolume(v); err != nil {
		t.Fatalf("AddVolume(%s/%s): %v", owner, name, err)
	}
	return v
}

// `POST /v1/launch-machine?owner=victim` with a body naming the attacker cleared
// authorization against the attacker's own org and then provisioned a machine on
// the VICTIM's provider credentials, billed to the victim.
//
// The provision now resolves the provider in the CALLER's org, which is what the
// error proves: it got past tenant resolution carrying the signed org and looked
// the provider up THERE.
func TestLaunchMachineUsesTheSignedOrgNotTheQuery(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)

	body := post(t, app, "/v1/launch-machine?owner=victimlaunch&provider=platformdo",
		mint("attackerlaunch"), `{"owner":"attackerlaunch","name":"m1","instanceType":"s-1vcpu-1gb"}`)

	if !strings.Contains(body, "attackerlaunch") {
		t.Fatalf("the launch must resolve the provider in the CALLER's org, got %s", body)
	}
	if strings.Contains(body, "victimlaunch") {
		t.Fatalf("the query's org reached the provision: %s", body)
	}
}

// Every volume WRITE takes its tenant from the token too. Each fails on the
// missing provider, and the org named in that failure is the proof: it is the
// caller's, never the query's.
//
// The write now resolves the volume ROW before it resolves a provider, so the
// row seeded here is the caller's own and the provider named on it is looked up
// in the caller's org. That is a second lock on the same door: a volume the
// caller does not own is not there to address (see the 404 below), and one it
// does own carries the only provider the call can reach.
func TestVolumeWritesUseTheSignedOrgNotTheQuery(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)
	storedVolume(t, "attackervol", "vol-1")

	for name, tc := range map[string]struct{ method, path, body string }{
		"create": {http.MethodPost, "/v1/volumes?owner=victimvol&provider=platformdo", `{"name":"vol-2","size":100}`},
		"delete": {http.MethodDelete, "/v1/volumes/vol-1?owner=victimvol", ``},
		"attach": {http.MethodPut, "/v1/volumes/vol-1/attachment?owner=victimvol", `{"machine":"m-1"}`},
		"detach": {http.MethodDelete, "/v1/volumes/vol-1/attachment?owner=victimvol", ``},
		"resize": {http.MethodPatch, "/v1/volumes/vol-1?owner=victimvol", `{"size":200}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, body := send(t, app, tc.method, tc.path, mint("attackervol"), tc.body)
			if !strings.Contains(body, "attackervol") {
				t.Fatalf("%s must resolve the provider in the CALLER's org, got %s", name, body)
			}
			if strings.Contains(body, "victimvol") {
				t.Fatalf("%s reached another org's provider credentials: %s", name, body)
			}
		})
	}
}

// A volume another org owns is not addressable at all: the id in the path is
// resolved against the CALLER's org, so it names nothing and the request stops
// at 404 without a provider ever being looked up.
func TestVolumeWritesCannotAddressAnotherOrgsVolume(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)
	storedVolume(t, "victimvolreach", "secret-disk")

	for name, tc := range map[string]struct{ method, path, body string }{
		"delete": {http.MethodDelete, "/v1/volumes/secret-disk?owner=victimvolreach", ``},
		"attach": {http.MethodPut, "/v1/volumes/secret-disk/attachment?owner=victimvolreach", `{"machine":"m-1"}`},
		"detach": {http.MethodDelete, "/v1/volumes/secret-disk/attachment?owner=victimvolreach", ``},
		"resize": {http.MethodPatch, "/v1/volumes/secret-disk?owner=victimvolreach", `{"size":200}`},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := send(t, app, tc.method, tc.path, mint("attackervolreach"), tc.body)
			if status != http.StatusNotFound {
				t.Fatalf("%s of another org's volume = %d %s, want 404", name, status, body)
			}
		})
	}
}

// The volume READS are scoped to the caller as well. Authorization keys a GET on
// `?id=`, so a request could name its own org there and another org's in
// `?owner=` — the filter judged the first and the handler read the second.
func TestVolumeReadsAreScopedToTheCaller(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)
	storedVolume(t, "victimvolread", "secret-disk")
	storedVolume(t, "attackervolread", "own-disk")

	list := get(t, app, "/v1/volumes?owner=victimvolread&id=attackervolread/own-disk", mint("attackervolread"))
	if strings.Contains(list, "secret-disk") || strings.Contains(list, "victimvolread") {
		t.Fatalf("another org's volumes were listed: %s", list)
	}
	if !strings.Contains(list, "own-disk") {
		t.Fatalf("the caller must still see its OWN volumes: %s", list)
	}

	status, one := send(t, app, http.MethodGet, "/v1/volumes/secret-disk?owner=victimvolread", mint("attackervolread"), "")
	if status != http.StatusNotFound {
		t.Fatalf("another org's volume was readable: %d %s", status, one)
	}
	if strings.Contains(one, "victimvolread") {
		t.Fatalf("the refusal named the other org: %s", one)
	}
}

// No bearer and no service credential means no tenant, and a tenant-less
// provision is a configured cloud account waiting to happen.
// Each body below is COMPLETE, so nothing is refused for being malformed and
// the missing tenant is the only thing left to refuse it.
func TestMachineAndVolumeWritesFailClosedWithoutAnOrg(t *testing.T) {
	app := tenantWire(t)
	if body := post(t, app, "/v1/launch-machine?provider=do", "", `{"name":"m1"}`); !strings.Contains(body, "no org context") {
		t.Fatalf("a tenant-less launch must be refused, got %s", body)
	}

	for name, tc := range map[string]struct{ method, path, body string }{
		"create": {http.MethodPost, "/v1/volumes?provider=do", `{"name":"v1","size":10}`},
		"delete": {http.MethodDelete, "/v1/volumes/v1", ``},
		"attach": {http.MethodPut, "/v1/volumes/v1/attachment", `{"machine":"m1"}`},
		"detach": {http.MethodDelete, "/v1/volumes/v1/attachment", ``},
		"resize": {http.MethodPatch, "/v1/volumes/v1", `{"size":20}`},
		"list":   {http.MethodGet, "/v1/volumes", ``},
		"get":    {http.MethodGet, "/v1/volumes/v1", ``},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := send(t, app, tc.method, tc.path, "", tc.body)
			if status != http.StatusForbidden || !strings.Contains(body, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %d %s", name, status, body)
			}
		})
	}
}
