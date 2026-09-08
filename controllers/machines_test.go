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

// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package controllers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
)

// machineWire stands the machine collection up exactly as routers.Route does.
// The shape is the subject as much as the handlers are: the owner is a PATH
// segment on the item and a query on the collection, and reading it from the
// wrong one is the defect these tests are for.
func machineWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	h := func(fn func(*ApiController)) zip.Handler {
		return func(c *zip.Ctx) error { fn(New(c)); return nil }
	}
	app.Get("/v1/machines", h((*ApiController).ListMachines))
	app.Get("/v1/machines/:owner/:name", h((*ApiController).GetMachine))
	return app
}

func storedMachine(t *testing.T, owner, name string) *object.Machine {
	t.Helper()
	now := time.Now().Format(time.RFC3339)
	m := &object.Machine{
		Owner: owner, Name: name, Id: "drop-" + name, Provider: "digitalocean",
		Region: "sfo3", Size: "s-2vcpu-4gb", State: "running",
		CreatedTime: now, DisplayName: name,
	}
	if _, err := object.AddMachine(m); err != nil {
		t.Fatalf("AddMachine(%s/%s): %v", owner, name, err)
	}
	return m
}

// THE REGISTRY IS A MIRROR, NOT A STORE, and this is the test that says so.
//
// Measured: a read runs object.SyncMachinesCloud, which DELETES the org's rows
// and re-adds whatever its providers report. So an org with no active provider
// reads empty no matter what the table held a moment earlier — two rows written
// directly, as here, are gone by the time the answer is composed.
//
// That is worth a test rather than a surprise. It is also why "source" exists:
// the registry half is whatever the org's OWN credentials can see, and calling
// it a separate collection was always a description of where the rows came
// from, never of a second kind of machine.
func TestReadingRebuildsTheRegistryFromTheOrgsProviders(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := machineWire(t)
	storedMachine(t, "sourceorg", "web-1")
	storedMachine(t, "sourceorg", "web-2")

	body := get(t, app, "/v1/machines?owner=sourceorg", mint("sourceorg"))
	if strings.Contains(body, "web-1") || strings.Contains(body, "web-2") {
		t.Fatalf("a row survived a read with no provider to justify it: %s", body)
	}
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("an org with nothing to list is an empty answer, not an error: %s", body)
	}
	// Nothing may be tagged live either: there is no house account here, and a
	// row appearing under that source would mean the tag was a default.
	if strings.Contains(body, `"source":"live"`) {
		t.Errorf("tagged a row live with no house account configured: %s", body)
	}
}

// A COLLECTION WITH NO TENANT IS REFUSED rather than answered from whatever the
// store happens to hold. The item address cannot be in this state — it names an
// org — so the collection is the only place the question arises.
func TestListMachinesFailsClosedWithoutAnOrg(t *testing.T) {
	if body := get(t, machineWire(t), "/v1/machines", ""); !strings.Contains(body, refuseNoOrg) {
		t.Fatalf("a tenant-less list must be refused, got %s", body)
	}
}

// THE ADDRESS NAMES WHICH MACHINE, THE TOKEN NAMES WHOSE. A signed caller
// aiming the path at another org reads its own org, and finds nothing there —
// never the victim's row.
func TestGetMachineIsScopedToTheCaller(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := machineWire(t)
	storedMachine(t, "victimread", "secret-box")
	storedMachine(t, "attackerread", "own-box")

	// Two reads by the same caller, differing ONLY in the org named in the path.
	// Both must be looked up in the CALLER's org: the address changes which
	// machine is asked for, never whose.
	//
	// A successful read is not available as a control here — the registry is
	// rebuilt on read and this org has no provider, so both answers are
	// refusals. The org NAMED in each refusal is what separates them, and it is
	// enough: were the owner taken from the path, the first would say
	// "in victimread".
	one := get(t, app, "/v1/machines/victimread/secret-box", mint("attackerread"))
	own := get(t, app, "/v1/machines/attackerread/own-box", mint("attackerread"))
	for what, body := range map[string]string{"another org's": one, "its own": own} {
		if !strings.Contains(body, "attackerread") {
			t.Errorf("%s machine was not looked up in the caller's org: %s", what, body)
		}
		if strings.Contains(body, "victimread") || strings.Contains(body, "drop-secret-box") {
			t.Errorf("%s read reached another org: %s", what, body)
		}
	}
}

// A MACHINE THAT IS NOT THERE SAYS SO, naming what was asked for and where it
// was looked for. "no machine x in y" is a sentence a caller can act on; an
// empty object is one it has to guess at.
func TestGetMachineSaysWhatIsMissing(t *testing.T) {
	mint := signer(t, "https://test.id")
	body := get(t, machineWire(t), "/v1/machines/absentorg/ghost", mint("absentorg"))
	if !strings.Contains(body, "no machine ghost in absentorg") {
		t.Fatalf("want a refusal naming the machine and the org, got %s", body)
	}
}

// The collection is a GET and so is the item — one address shape, learned once.
// A POST reaching either read is a routing mistake, not a create.
func TestMachineReadsAreGets(t *testing.T) {
	app := machineWire(t)
	for _, path := range []string{"/v1/machines", "/v1/machines/acme/web-1"} {
		if status, _ := ask(t, app, http.MethodPost, path, "", ""); status != http.StatusMethodNotAllowed &&
			status != http.StatusNotFound {
			t.Errorf("POST %s answered %d; the reads are GET only", path, status)
		}
	}
}
