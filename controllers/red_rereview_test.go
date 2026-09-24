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
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/service/ec2test"
)

// The C1 rule — a tenant's own provider row is never carried under compute's
// identity — is kept by Provider.credential setting Tenant. Volumes, VPCs and
// load balancers build their Credential from the row's fields instead
// (service/volume.go, vpc.go, loadbalancer.go: no Name, no Tenant), so a
// tenant's row reaches the carrier as a platform account labelled "default".
// Egress then spends compute's custody for <provider>/default, and the
// platform's account there the day EGRESS_PLATFORM lists that label.
func TestRedATenantsVolumeRowIsNeverCarried(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []service.Credential
	)
	service.RegisterCarrier(func(c service.Credential) (*http.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, c)
		return nil, errors.New("recorded")
	})
	t.Cleanup(func() {
		service.RegisterCarrier(func(c service.Credential) (*http.Client, error) { return hosted.Client(c.Provider, c.Name), nil })
	})

	if _, err := object.AddProvider(&object.Provider{
		Owner: "mallory2", Name: "do", Category: "Cloud", Type: "DigitalOcean",
		ClientSecret: "dop_mallory", Region: "nyc3", State: "Active",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := object.CreateVolumeCloud("mallory2", "do", &service.CreateVolumeSpec{Name: "v", Size: 10, Region: "nyc3"})

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Fatalf("mallory2's own DigitalOcean row was handed to the carrier as %+v (err %v); want %v before the carrier",
			seen[0], err, service.ErrTenantNotCarried)
	}
}

// A row owned by platformOwner is carried as the platform's own account, under
// whatever label its name says. Any member of that org can write such a row —
// the Casbin rule is subOwner == objOwner, with no role — so a member who names
// an AWS row hanzo-compute drives the hosted account through the BYO path: the
// org's machine list syncs every tenant's instance into its rows, and a state
// write stops one. The hosted account is reached by hostedLabel alone; a row
// is never the way to it.
func TestRedAPlatformOrgRowIsNotTheHostedAccount(t *testing.T) {
	app := hostedWire(t)
	t.Setenv("platformOwner", "hanzo")
	victim := hosted.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: map[string]string{
		"hanzo-org": "acme", "hanzo-machine": "m-bbbbbbbbbbbbbbbbbbbb", "managed-by": "hanzo-compute",
	}})
	if _, err := object.AddProvider(&object.Provider{
		Owner: "hanzo", Name: "hanzo-compute", Category: "Cloud", Type: "AWS",
		ClientId: "AKIAANYMEMBER0000000", ClientSecret: "anything", Region: ec2test.Region, State: "Active",
	}); err != nil {
		t.Fatal(err)
	}

	_, body := ask(t, app, http.MethodGet, "/v1/machines?owner=hanzo", "", "")
	if strings.Contains(body, victim.ID) {
		t.Errorf("org hanzo's machine list names acme's instance %s: a provider row reached the hosted account", victim.ID)
	}
	_, _ = ask(t, app, http.MethodPut, "/v1/machines/hanzo/"+victim.ID, "", `{"state":"Stopped"}`)
	for _, c := range hosted.Calls("StopInstances") {
		if c.Form.Get("InstanceId.1") == victim.ID {
			t.Fatalf("a row in the platform org stopped acme's hosted machine %s", victim.ID)
		}
	}
}

// A launch is gated on the first hour and nothing after it. The sweep records
// every later hour whatever the balance ("enforcement/suspend on a depleted
// balance is a separate control" — service/metering.go — and no such control
// exists), so an org funded for one hour of a p5.48xlarge ($73.54) runs it on
// Hanzo's AWS bill for as long as it likes.
func TestRedAMachineItsOrgCannotPayForIsStopped(t *testing.T) {
	app := hostedWire(t)
	money := commerceWith(t, 138) // exactly one g5.xlarge hour

	env := call(t, app, http.MethodPost, "/v1/machines?owner=acme", cloudLaunch{Name: "gpu", Size: "g5.xlarge", Kind: "machine"})
	if env.Status != "ok" {
		t.Fatalf("launch: %+v", env)
	}
	inst := hosted.Instances()[0]
	hosted.SetState(inst.ID, "running")

	for h := 1; h <= 3; h++ {
		service.MeterRunningMachines(context.Background(), time.Now().Add(time.Duration(h)*time.Hour))
	}

	money.mu.Lock()
	debits := len(money.debits)
	money.mu.Unlock()
	for _, c := range hosted.Calls("StopInstances") {
		if c.Form.Get("InstanceId.1") == inst.ID {
			return
		}
	}
	t.Fatalf("acme paid for one hour (138 cents) and its g5.xlarge was debited %d hours and is still running", debits)
}
