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

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service/ec2test"
)

// A tenant's own AWS provider row is carried to egress under COMPUTE's token,
// with the row's name as the account label (object/provider.go credential,
// service/machine.go NewMachineClient "AWS" under a carrier). Egress resolves
// that label for compute's principal: compute's own path is empty, compute's
// org is hanzo, so it falls to cloud/aws/<label>/credential — the platform
// account when the tenant names its row hanzo-compute. The stand-in egress
// accepts exactly what the real one would (compute's token, label
// hanzo-compute, the admitted endpoint), so what it answers here is what
// Hanzo's role would answer.
//
// The chain: the org's machine list syncs every instance in Hanzo's account
// into the tenant's rows (DescribeInstances with no filter), and a state write
// on one of those rows stops another tenant's hosted machine as hanzo-compute.
func TestRedATenantsAWSRowIsNeverThePlatformAccount(t *testing.T) {
	app := hostedWire(t)
	victim := hosted.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: map[string]string{
		"hanzo-org": "acme", "hanzo-machine": "m-aaaaaaaaaaaaaaaaaaaa", "managed-by": "hanzo-compute",
	}})

	if _, err := object.AddProvider(&object.Provider{
		Owner: "mallory", Name: "hanzo-compute", Category: "Cloud", Type: "AWS",
		ClientId: "AKIAMALLORY000000000", ClientSecret: "anything", Region: ec2test.Region, State: "Active",
	}); err != nil {
		t.Fatal(err)
	}

	status, body := ask(t, app, http.MethodGet, "/v1/machines?owner=mallory", "", "")
	if strings.Contains(body, victim.ID) {
		t.Errorf("mallory's machine list names acme's instance %s (status %d): %s", victim.ID, status, body)
	}

	_, _ = ask(t, app, http.MethodPut, "/v1/machines/mallory/"+victim.ID, "", `{"state":"Stopped"}`)
	for _, c := range hosted.Calls("StopInstances") {
		if c.Form.Get("InstanceId.1") == victim.ID {
			t.Fatalf("mallory stopped acme's hosted machine %s through Hanzo's account", victim.ID)
		}
	}
	for _, c := range hosted.Calls("") {
		if c.Refused == "" && c.Action == "DescribeInstances" && len(c.Form["Filter.1.Name"]) == 0 {
			t.Fatalf("a tenant's provider row listed Hanzo's whole account through egress as %s", c.Fetch.Label)
		}
	}
}

// A launch debits its first hour under the machine id; a start debits the
// start's clock hour under MeterID. A machine launched, stopped and started in
// one clock hour is therefore charged that hour twice. The sweep's skip rule
// (CreatedInHour) is what keeps launch and sweep apart; nothing keeps launch
// and start apart.
func TestRedALaunchHourIsNotChargedAgainByAStart(t *testing.T) {
	app := hostedWire(t)
	money := commerceWith(t, 1_000_000)

	env := call(t, app, http.MethodPost, "/v1/machines?owner=acme", cloudLaunch{Name: "web-1", Size: "t3.medium", Kind: "machine"})
	if env.Status != "ok" {
		t.Fatalf("launch: %+v", env)
	}
	inst := hosted.Instances()[0]
	id := inst.Tags["hanzo-machine"]
	hosted.SetState(inst.ID, "running")

	if env := call(t, app, http.MethodPut, "/v1/machines/acme/"+id, map[string]string{"state": "Stopped"}); env.Status != "ok" {
		t.Fatalf("stop: %+v", env)
	}
	hosted.SetState(inst.ID, "stopped")
	if env := call(t, app, http.MethodPut, "/v1/machines/acme/"+id, map[string]string{"state": "Running"}); env.Status != "ok" {
		t.Fatalf("start: %+v", env)
	}

	money.mu.Lock()
	defer money.mu.Unlock()
	var cents int64
	for _, d := range money.debits {
		cents += d.Cents()
	}
	if len(money.debits) != 1 {
		t.Fatalf("one clock hour of t3.medium was debited %d times (%d cents): %+v", len(money.debits), cents, money.debits)
	}
}
