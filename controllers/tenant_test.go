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

	"github.com/hanzoai/compute/object"
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
	app.Post("/v1/machines", handler((*ApiController).LaunchComputeMachine))
	app.Get("/v1/volumes", handler((*ApiController).GetVolumes))
	app.Post("/v1/volumes", handler((*ApiController).CreateVolume))
	app.Get("/v1/volumes/:owner/:name", handler((*ApiController).GetVolume))
	app.Delete("/v1/volumes/:owner/:name", handler((*ApiController).DeleteVolume))
	app.Put("/v1/volumes/:owner/:name/attachment", handler((*ApiController).AttachVolume))
	app.Delete("/v1/volumes/:owner/:name/attachment", handler((*ApiController).DetachVolume))
	app.Put("/v1/volumes/:owner/:name/size", handler((*ApiController).ResizeVolume))
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

// `?owner=victim` with a body naming the attacker cleared authorization against
// the attacker's own org and then provisioned on the VICTIM's provider
// credentials, billed to the victim.
//
// The org a compute call runs as is decided in ONE place, so that place is the
// subject here rather than any single handler: an error message that happens to
// name an org is a weaker proof, and it disappears the moment a handler refuses
// earlier for an unrelated reason.
func TestComputeOrgComesFromTheTokenNotTheAddress(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	echo := func(c *zip.Ctx) error { return c.String(http.StatusOK, New(c).resolveComputeOrg()) }
	app.Get("/v1/machines", echo)
	app.Get("/v1/machines/:owner/:name", echo)

	if got := get(t, app, "/v1/machines?owner=victimlaunch", mint("attackerlaunch")); got != "attackerlaunch" {
		t.Errorf("the query's org won: %q", got)
	}
	// The address is the newer vector: the owner moved out of the query and into
	// a path segment, and it must not have gained authority on the way.
	if got := get(t, app, "/v1/machines/victimlaunch/m1", mint("attackerlaunch")); got != "attackerlaunch" {
		t.Errorf("the address's org won: %q", got)
	}
	// The control, and the actual contract: with no token there is nothing to
	// override the address, so the address resolves. That is why an
	// unauthenticated request must never reach a handler — routers/health_test.go
	// is what holds that end.
	if got := get(t, app, "/v1/machines?owner=victimlaunch", ""); got != "victimlaunch" {
		t.Errorf("without a token the address must resolve, got %q", got)
	}
}

// Every volume WRITE takes its tenant from the token too. Each fails on the
// missing provider, and the org named in that failure is the proof: it is the
// caller's, never the query's.
func TestVolumeWritesUseTheSignedOrgNotTheQuery(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)

	// The victim's org is now named in the ADDRESS, which is the stronger form of
	// the same attack: the caller is asking for a path it has no claim to.
	for name, tc := range map[string]struct{ method, path string }{
		"create": {http.MethodPost, "/v1/volumes?owner=victimvol&provider=platformdo"},
		"delete": {http.MethodDelete, "/v1/volumes/victimvol/vol-1?provider=platformdo"},
		"attach": {http.MethodPut, "/v1/volumes/victimvol/vol-1/attachment?provider=platformdo&machine=m-1"},
		"detach": {http.MethodDelete, "/v1/volumes/victimvol/vol-1/attachment?provider=platformdo"},
		"resize": {http.MethodPut, "/v1/volumes/victimvol/vol-1/size?provider=platformdo&size=200"},
	} {
		t.Run(name, func(t *testing.T) {
			_, body := ask(t, app, tc.method, tc.path, mint("attackervol"), `{"owner":"attackervol","name":"vol-1","sizeGb":100}`)
			if !strings.Contains(body, "attackervol") {
				t.Fatalf("%s must resolve the provider in the CALLER's org, got %s", name, body)
			}
			if strings.Contains(body, "victimvol") {
				t.Fatalf("%s reached another org's provider credentials: %s", name, body)
			}
		})
	}
}

// The volume READS are scoped to the caller as well. The owner is now a PATH
// segment, which is the same segment the authorization seam reads, so the two
// can no longer disagree — a caller naming another org's volume in the address
// is judged on that address. This asks for exactly that.
func TestVolumeReadsAreScopedToTheCaller(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := tenantWire(t)
	storedVolume(t, "victimvolread", "secret-disk")
	storedVolume(t, "attackervolread", "own-disk")

	list := get(t, app, "/v1/volumes?owner=victimvolread", mint("attackervolread"))
	if strings.Contains(list, "secret-disk") || strings.Contains(list, "victimvolread") {
		t.Fatalf("another org's volumes were listed: %s", list)
	}
	if !strings.Contains(list, "own-disk") {
		t.Fatalf("the caller must still see its OWN volumes: %s", list)
	}

	one := get(t, app, "/v1/volumes/victimvolread/secret-disk", mint("attackervolread"))
	if strings.Contains(one, "secret-disk") || strings.Contains(one, "victimvolread") {
		t.Fatalf("another org's volume was readable: %s", one)
	}
}

// No bearer and no service credential means no tenant, and a tenant-less
// provision is a configured cloud account waiting to happen.
func TestMachineAndVolumeWritesFailClosedWithoutAnOrg(t *testing.T) {
	app := tenantWire(t)
	for name, tc := range map[string]struct{ method, path, body string }{
		"launch": {http.MethodPost, "/v1/machines?provider=do", `{"name":"m1"}`},
		"create": {http.MethodPost, "/v1/volumes?provider=do", `{"name":"v1","sizeGb":10}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, body := ask(t, app, tc.method, tc.path, "", tc.body)
			if !strings.Contains(body, refuseNoOrg) {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}

	// Only the collection. An item address NAMES its org, so "no org context" is
	// not a state it can be in — what must be refused there is reading ANOTHER
	// org's item, which is the test above and the filter in front of it.
	for name, path := range map[string]string{
		"list": "/v1/volumes",
	} {
		t.Run(name, func(t *testing.T) {
			if body := get(t, app, path, ""); !strings.Contains(body, refuseNoOrg) {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, body)
			}
		})
	}
}
