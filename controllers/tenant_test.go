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
	app.Get("/v1/get-volumes", handler((*ApiController).GetVolumes))
	app.Get("/v1/get-volume", handler((*ApiController).GetVolume))
	app.Post("/v1/create-volume", handler((*ApiController).CreateVolume))
	app.Post("/v1/delete-volume", handler((*ApiController).DeleteVolume))
	app.Post("/v1/attach-volume", handler((*ApiController).AttachVolume))
	app.Post("/v1/detach-volume", handler((*ApiController).DetachVolume))
	app.Post("/v1/resize-volume", handler((*ApiController).ResizeVolume))
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

	body := post(t, app, "/v1/launch-machine?owner=victimlaunch&provider=housedo",
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
func TestVolumeWritesUseTheSignedOrgNotTheQuery(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)

	for name, path := range map[string]string{
		"create": "/v1/create-volume?owner=victimvol&provider=housedo",
		"delete": "/v1/delete-volume?owner=victimvol&provider=housedo&name=vol-1",
		"attach": "/v1/attach-volume?owner=victimvol&provider=housedo&volume=vol-1&machine=m-1",
		"detach": "/v1/detach-volume?owner=victimvol&provider=housedo&volume=vol-1",
		"resize": "/v1/resize-volume?owner=victimvol&provider=housedo&volume=vol-1&size=200",
	} {
		t.Run(name, func(t *testing.T) {
			body := post(t, app, path, mint("attackervol"), `{"owner":"attackervol","name":"vol-1","sizeGb":100}`)
			if !strings.Contains(body, "attackervol") {
				t.Fatalf("%s must resolve the provider in the CALLER's org, got %s", name, body)
			}
			if strings.Contains(body, "victimvol") {
				t.Fatalf("%s reached another org's provider credentials: %s", name, body)
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

	list := get(t, app, "/v1/get-volumes?owner=victimvolread&id=attackervolread/own-disk", mint("attackervolread"))
	if strings.Contains(list, "secret-disk") || strings.Contains(list, "victimvolread") {
		t.Fatalf("another org's volumes were listed: %s", list)
	}
	if !strings.Contains(list, "own-disk") {
		t.Fatalf("the caller must still see its OWN volumes: %s", list)
	}

	one := get(t, app, "/v1/get-volume?owner=victimvolread&name=secret-disk", mint("attackervolread"))
	if strings.Contains(one, "secret-disk") || strings.Contains(one, "victimvolread") {
		t.Fatalf("another org's volume was readable: %s", one)
	}
}

// No bearer and no service credential means no tenant, and a tenant-less
// provision is a house account waiting to happen.
func TestMachineAndVolumeWritesFailClosedWithoutAnOrg(t *testing.T) {
	app := tenantWire(t)
	for name, tc := range map[string]struct{ path, body string }{
		"launch": {"/v1/launch-machine?provider=do", `{"name":"m1"}`},
		"create": {"/v1/create-volume?provider=do", `{"name":"v1","sizeGb":10}`},
		"delete": {"/v1/delete-volume?provider=do&name=v1", ``},
		"attach": {"/v1/attach-volume?provider=do&volume=v1&machine=m1", ``},
		"detach": {"/v1/detach-volume?provider=do&volume=v1", ``},
		"resize": {"/v1/resize-volume?provider=do&volume=v1&size=20", ``},
	} {
		t.Run(name, func(t *testing.T) {
			body := post(t, app, tc.path, "", tc.body)
			if !strings.Contains(body, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}

	for name, path := range map[string]string{
		"list": "/v1/get-volumes",
		"get":  "/v1/get-volume?name=v1",
	} {
		t.Run(name, func(t *testing.T) {
			if body := get(t, app, path, ""); !strings.Contains(body, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}
}
