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

package service

// These tests drive the hosted account through the real EC2 SDK client and the
// real spend carrier, against ec2test's stand-in egress and the fake account
// behind it: every call is serialized exactly as it would be for AWS, described
// to egress over ZAP, and the assertions read what arrived.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/egress/spend"
	"github.com/hanzoai/iamsdk/v2/iamsdk"

	"github.com/hanzoai/compute/service/commercetest"
	"github.com/hanzoai/compute/service/ec2test"
)

// hostedFake points the hosted account at a fresh fake account for one test and
// carries every cloud call through the stand-in egress in front of it.
func hostedFake(t *testing.T) *ec2test.Fake {
	t.Helper()
	f := ec2test.Serve(t)
	freshLedger(t)
	RegisterCarrier(func(c Credential) (*http.Client, error) { return f.Client(c.Provider, c.Name), nil })
	t.Cleanup(func() { RegisterCarrier(nil) })
	return f
}

// botRegistries answers the IAM user and playground node registrations a bot
// launch makes, so a bot launches with nothing reached outside the test.
func botRegistries(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","data":"Affected"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PLAYGROUND_URL", srv.URL)
	iamsdk.InitConfig(srv.URL, "hanzo-visor", "secret", "", "hanzo", "hanzo-visor")
	t.Cleanup(func() { iamsdk.InitConfig("", "", "", "", "", "") })
}

func launchOf(t *testing.T, f *ec2test.Fake) ec2test.Call {
	t.Helper()
	runs := f.Calls("RunInstances")
	if len(runs) != 1 {
		t.Fatalf("RunInstances calls = %d, want 1", len(runs))
	}
	return runs[0]
}

// instanceTags reads the tags a RunInstances put on resourceType.
func instanceTags(c ec2test.Call, resourceType string) map[string]string {
	tags := map[string]string{}
	for i := 1; ; i++ {
		p := "TagSpecification." + itoa(i)
		rt := c.Form.Get(p + ".ResourceType")
		if rt == "" {
			return tags
		}
		if rt != resourceType {
			continue
		}
		for j := 1; c.Form.Has(p + ".Tag." + itoa(j) + ".Key"); j++ {
			tags[c.Form.Get(p+".Tag."+itoa(j)+".Key")] = c.Form.Get(p + ".Tag." + itoa(j) + ".Value")
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// A launch tags the instance AND its root volume with the org and the machine id
// that own it — the two tags every read filters on and the meter bills by — and
// nothing a customer wrote.
func TestLaunchTagsTheMachineWithItsOrgAndID(t *testing.T) {
	f := hostedFake(t)

	spec := &CreateMachineSpec{Name: "web-1", InstanceType: "t3.medium", Tags: map[string]string{
		"team":        "platform",               // a customer tag: not written to the account
		orgTagKey:     "victim",                 // a forged org: replaced by the resolved one
		machineTagKey: "m-00000000000000000000", // a forged id: replaced by the minted one
	}}
	SetKind(spec, KindTab)
	SetScope(spec, "console", "web")

	m, err := LaunchOrgMachine(context.Background(), "acme", "web", spec)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !machineIDPattern.MatchString(m.Id) || m.Name != m.Id {
		t.Fatalf("machine id = %q name = %q, want one minted id in both", m.Id, m.Name)
	}
	if m.Owner != "acme" || m.DisplayName != "web-1" || m.Size != "t3.medium" || m.Region != ec2test.Region {
		t.Fatalf("machine = %+v", m)
	}
	if m.State != "Starting" || m.CpuSize != "2" || m.MemSize != "4096" {
		t.Fatalf("machine state/shape = %q %q %q, want Starting 2 4096", m.State, m.CpuSize, m.MemSize)
	}

	run := launchOf(t, f)
	want := map[string]string{
		"Name":        "web-1",
		orgTagKey:     "acme",
		machineTagKey: m.Id,
		projectTagKey: "web",
		appTagKey:     "console",
		kindTagKey:    KindTab,
		managedByKey:  managedBy,
	}
	for _, resource := range []string{"instance", "volume"} {
		got := instanceTags(run, resource)
		if len(got) != len(want) {
			t.Errorf("%s tags = %v, want exactly %v", resource, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s tag %s = %q, want %q", resource, k, got[k], v)
			}
		}
	}
	if got := orgFromTag(m.Tag); got != "acme" {
		t.Fatalf("the meter reads org %q off the machine, want acme (tags %q)", got, m.Tag)
	}
	if spec.Tags[orgTagKey] != "victim" {
		t.Fatal("the launch wrote through the caller's tag map")
	}
}

// The launch request itself: the configured network, the configured image, the
// minted id as the idempotency token, a root volume that is the size the price
// includes and is encrypted and deleted with the instance, IMDSv2 required, no
// IAM role, no key pair, and standard credits on a burstable type.
func TestLaunchAsksForExactlyTheConfiguredMachine(t *testing.T) {
	f := hostedFake(t)

	m, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "t3.medium"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	run := launchOf(t, f).Form
	for key, want := range map[string]string{
		"ImageId":                             ec2test.Image,
		"InstanceType":                        "t3.medium",
		"MinCount":                            "1",
		"MaxCount":                            "1",
		"ClientToken":                         m.Id,
		"SubnetId":                            ec2test.Subnet,
		"SecurityGroupId.1":                   ec2test.SecurityGroup,
		"BlockDeviceMapping.1.DeviceName":     "/dev/sda1",
		"BlockDeviceMapping.1.Ebs.VolumeSize": "50",
		"BlockDeviceMapping.1.Ebs.VolumeType": "gp3",
		"BlockDeviceMapping.1.Ebs.Encrypted":  "true",
		"BlockDeviceMapping.1.Ebs.DeleteOnTermination": "true",
		"MetadataOptions.HttpTokens":                   "required",
		"CreditSpecification.CpuCredits":               "standard",
	} {
		if got := run.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for key := range run {
		if strings.HasPrefix(key, "IamInstanceProfile") || key == "KeyName" || strings.HasPrefix(key, "NetworkInterface") {
			t.Errorf("the launch sent %s: a hosted machine carries no role, key pair or extra interface", key)
		}
	}
	if run.Has("UserData") {
		t.Error("a plain machine boots no agent, so it carries no user data")
	}
}

// A GPU size boots the GPU image, with the GPU size's root volume and no credit
// setting; a bot carries its bootstrap as user data, naming its org.
func TestAGPUBotBootsTheGPUImageAndItsAgent(t *testing.T) {
	f := hostedFake(t)
	botRegistries(t)

	spec := &CreateMachineSpec{Name: "trainer", InstanceType: "g5.xlarge"}
	SetKind(spec, KindBot)
	if _, err := LaunchOrgMachine(context.Background(), "acme", "", spec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	run := launchOf(t, f).Form
	if run.Get("ImageId") != ec2test.GPUImage {
		t.Errorf("ImageId = %q, want the GPU image", run.Get("ImageId"))
	}
	if run.Get("BlockDeviceMapping.1.Ebs.VolumeSize") != "200" {
		t.Errorf("root volume = %q GB, want 200", run.Get("BlockDeviceMapping.1.Ebs.VolumeSize"))
	}
	if run.Has("CreditSpecification.CpuCredits") {
		t.Error("only a burstable type takes a credit setting")
	}
	boot, err := base64.StdEncoding.DecodeString(run.Get("UserData"))
	if err != nil {
		t.Fatalf("user data is not base64: %v", err)
	}
	if !strings.Contains(string(boot), "HANZO_ORG=acme") || !strings.Contains(string(boot), "npm install -g @hanzo/bot") {
		t.Fatalf("a bot's user data must install the agent for its org:\n%s", boot)
	}
}

// Every setting a launch needs is named when it is missing, and nothing is asked
// of the cloud: a launch never guesses a region, a network or an image.
func TestLaunchRefusesWhenConfigIsMissing(t *testing.T) {
	for _, tc := range []struct {
		unset string
		size  string
	}{
		{keyRegion, "t3.medium"},
		{keySubnet, "t3.medium"},
		{keySecurityGroup, "g5.xlarge"},
		{keyImage, "t3.medium"},
		{keyGPUImage, "g5.xlarge"},
	} {
		t.Run(tc.unset, func(t *testing.T) {
			f := hostedFake(t)
			t.Setenv(tc.unset, "")

			_, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: tc.size})
			if err == nil || !strings.Contains(err.Error(), tc.unset) {
				t.Fatalf("launch without %s = %v, want a refusal naming it", tc.unset, err)
			}
			if calls := f.Calls(""); len(calls) != 0 {
				t.Fatalf("a refused launch asked the cloud %d times", len(calls))
			}
		})
	}

	// The image a size does not use is not required: a CPU launch needs no GPU image.
	t.Run("cpu without gpu image", func(t *testing.T) {
		hostedFake(t)
		t.Setenv(keyGPUImage, "")
		if _, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "t3.medium"}); err != nil {
			t.Fatalf("a CPU launch refused over the GPU image: %v", err)
		}
	})

	// With nothing configured there is no account at all.
	t.Run("unconfigured", func(t *testing.T) {
		hostedFake(t)
		t.Setenv(keyRegion, "")
		if ComputeConfigured() {
			t.Fatal("no region must read as unconfigured")
		}
		if _, err := ListOrgMachines("acme", ""); err == nil || !strings.Contains(err.Error(), keyRegion) {
			t.Fatalf("listing with no region = %v, want a refusal naming %s", err, keyRegion)
		}
	})
}

// What an account the caller does not own cannot honour is refused by name.
func TestLaunchRefusesWhatTheAccountCannotHonour(t *testing.T) {
	for name, spec := range map[string]CreateMachineSpec{
		"image":  {Name: "box", InstanceType: "t3.medium", ImageID: "ami-0mine"},
		"keys":   {Name: "box", InstanceType: "t3.medium", SSHKeyIDs: []string{"my-key"}},
		"region": {Name: "box", InstanceType: "t3.medium", Region: "sfo3"},
		"size":   {Name: "box", InstanceType: "s-2vcpu-4gb"},
	} {
		t.Run(name, func(t *testing.T) {
			f := hostedFake(t)
			if _, err := LaunchOrgMachine(context.Background(), "acme", "", &spec); err == nil {
				t.Fatal("launch was accepted")
			}
			if len(f.Calls("")) != 0 {
				t.Fatal("a refused launch reached the cloud")
			}
		})
	}
}

// An org and a project are exact EC2 filter values. A wildcard in either would
// widen a read to other tenants, so it is refused before any call.
func TestAWildcardNeverReachesAFilter(t *testing.T) {
	f := hostedFake(t)
	for _, org := range []string{"*", "ac?e", `a\b`} {
		if _, err := ListOrgMachines(org, ""); err == nil {
			t.Errorf("ListOrgMachines(%q) was accepted", org)
		}
		if _, err := LaunchOrgMachine(context.Background(), org, "", &CreateMachineSpec{Name: "b", InstanceType: "t3.medium"}); err == nil {
			t.Errorf("LaunchOrgMachine(%q) was accepted", org)
		}
	}
	if _, err := ListOrgMachines("acme", "*"); err == nil {
		t.Error("a wildcard project was accepted")
	}
	if n := len(f.Calls("")); n != 0 {
		t.Fatalf("a wildcard reached the cloud in %d calls", n)
	}
}

// One tenant never sees, stops or terminates another's machine — by listing, by
// guessing its id, or by naming it in the address.
func TestOneOrgNeverReachesAnothersMachine(t *testing.T) {
	f := hostedFake(t)
	ctx := context.Background()
	mine, err := LaunchOrgMachine(ctx, "acme", "", &CreateMachineSpec{Name: "mine", InstanceType: "t3.medium"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := LaunchOrgMachine(ctx, "beta", "", &CreateMachineSpec{Name: "theirs", InstanceType: "t3.medium"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := ListOrgMachines("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Id != mine.Id {
		t.Fatalf("acme lists %+v, want only its own machine", list)
	}
	if m, err := GetOrgMachine("acme", theirs.Id); err != nil || m != nil {
		t.Fatalf("acme read beta's machine: %+v, %v", m, err)
	}
	if err := DeleteOrgMachine("acme", theirs.Id); !errors.Is(err, ErrNoMachine) {
		t.Fatalf("acme deleting beta's machine = %v, want ErrNoMachine", err)
	}
	if _, err := SetOrgMachineState(ctx, "acme", theirs.Id, "Stopped"); !errors.Is(err, ErrNoMachine) {
		t.Fatalf("acme stopping beta's machine = %v, want ErrNoMachine", err)
	}
	if n := len(f.Calls("TerminateInstances")) + len(f.Calls("StopInstances")); n != 0 {
		t.Fatalf("another tenant's machine was acted on %d times", n)
	}

	// A machine whose tags say another org is not the caller's, whatever the
	// filter matched.
	f.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: map[string]string{
		orgTagKey: "beta", machineTagKey: "m-aaaaaaaaaaaaaaaaaaaa", managedByKey: managedBy,
	}})
	if m, _ := GetOrgMachine("acme", "m-aaaaaaaaaaaaaaaaaaaa"); m != nil {
		t.Fatal("a machine tagged for beta answered to acme")
	}
}

// Stop, start and terminate reach exactly the instance the machine id names. A
// start is metered like a launch: authorized, then debited under the hour's
// meter id — the id the sweep would use — so the two can never bill one hour
// twice. A stop charges nothing.
func TestStopStartAndTerminate(t *testing.T) {
	f := hostedFake(t)
	ctx := context.Background()
	m, err := LaunchOrgMachine(ctx, "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "m7i.large"})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := f.Instances()[0].ID

	var mu sync.Mutex
	var debits []commercetest.Usage
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_ = json.NewEncoder(w).Encode(map[string]any{"available": 100000, "currency": "usd"})
		case strings.HasSuffix(r.URL.Path, "/usage"):
			mu.Lock()
			debits = append(debits, commercetest.Read(r))
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	// The fake launches pending; it is running once the cloud says so.
	f.SetState(instanceID, "running")

	if changed, err := SetOrgMachineState(ctx, "acme", m.Id, "Running"); err != nil || changed {
		t.Fatalf("setting a running machine Running = %v, %v, want no change", changed, err)
	}
	if changed, err := SetOrgMachineState(ctx, "acme", m.Id, "Stopped"); err != nil || !changed {
		t.Fatalf("stop = %v, %v", changed, err)
	}
	if stops := f.Calls("StopInstances"); len(stops) != 1 || stops[0].Form.Get("InstanceId.1") != instanceID {
		t.Fatalf("stop reached %+v, want exactly %s", stops, instanceID)
	}
	if len(debits) != 0 {
		t.Fatalf("a stop charged %d times", len(debits))
	}

	if changed, err := SetOrgMachineState(ctx, "acme", m.Id, "running"); err != nil || !changed {
		t.Fatalf("start = %v, %v", changed, err)
	}
	if starts := f.Calls("StartInstances"); len(starts) != 1 || starts[0].Form.Get("InstanceId.1") != instanceID {
		t.Fatalf("start reached %+v, want exactly %s", starts, instanceID)
	}
	mu.Lock()
	if len(debits) != 1 || debits[0].Cents() != 15 || debits[0].Org != "acme" || !strings.HasPrefix(debits[0].ID, "compute-"+m.Id+"-") {
		t.Fatalf("start debits = %+v, want one 15-cent debit under the machine's meter id", debits)
	}
	if debits[0].ID != MeterID(m.Id, time.Now()) {
		t.Fatalf("start debited %q, want the hour's meter id %q", debits[0].ID, MeterID(m.Id, time.Now()))
	}
	mu.Unlock()

	if _, err := SetOrgMachineState(ctx, "acme", m.Id, "Rebooting"); err == nil {
		t.Fatal("a state other than Running or Stopped was accepted")
	}

	if err := DeleteOrgMachine("acme", m.Id); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if ends := f.Calls("TerminateInstances"); len(ends) != 1 || ends[0].Form.Get("InstanceId.1") != instanceID {
		t.Fatalf("terminate reached %+v, want exactly %s", ends, instanceID)
	}
	if m, _ := GetOrgMachine("acme", m.Id); m != nil && m.State != "Terminating" {
		t.Fatalf("a terminated machine reads %q", m.State)
	}
}

// A start the org cannot pay for starts nothing.
func TestAStartTheOrgCannotPayForStartsNothing(t *testing.T) {
	f := hostedFake(t)
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": 0, "currency": "usd"})
	}))
	f.Add(ec2test.Instance{Type: "g5.xlarge", State: "stopped", Tags: map[string]string{
		orgTagKey: "acme", machineTagKey: "m-bbbbbbbbbbbbbbbbbbbb", managedByKey: managedBy,
	}})
	if _, err := SetOrgMachineState(context.Background(), "acme", "m-bbbbbbbbbbbbbbbbbbbb", "Running"); err == nil || !strings.Contains(err.Error(), "balance") {
		t.Fatalf("an unfunded start = %v, want a balance refusal", err)
	}
	if n := len(f.Calls("StartInstances")); n != 0 {
		t.Fatalf("an unfunded start reached the cloud %d times", n)
	}
}

// The meter sees every running machine hosted compute launched, in every org,
// and nothing else: a stopped machine is not running, an instance the service
// did not launch is not its to bill, and a running instance with no machine id
// has no meter id to bill under.
func TestTheMeterSeesRunningHostedMachinesOnly(t *testing.T) {
	f := hostedFake(t)
	running := func(org, id string) map[string]string {
		return map[string]string{orgTagKey: org, machineTagKey: id, managedByKey: managedBy}
	}
	f.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: running("acme", "m-11111111111111111111")})
	f.Add(ec2test.Instance{Type: "g5.xlarge", State: "running", Tags: running("beta", "m-22222222222222222222")})
	f.Add(ec2test.Instance{Type: "t3.medium", State: "stopped", Tags: running("acme", "m-33333333333333333333")})
	f.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: map[string]string{orgTagKey: "acme"}})
	f.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: map[string]string{orgTagKey: "acme", managedByKey: managedBy}})

	got, err := ListMeteredMachines()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, m := range got {
		ids[m.Id] = orgFromTag(m.Tag)
	}
	if len(ids) != 3 || ids["m-11111111111111111111"] != "acme" || ids["m-22222222222222222222"] != "beta" ||
		ids["m-33333333333333333333"] != "acme" {
		t.Fatalf("metered = %v, want the running and stopped hosted machines with their orgs", ids)
	}
	for _, m := range got {
		if want := map[string]string{"m-33333333333333333333": "Stopped"}[m.Id]; want != "" && m.State != want {
			t.Fatalf("%s reads %q", m.Id, m.State)
		}
	}
}

// A GPU family starts at a quota of zero, so every GPU launch is refused until
// AWS grants one. That refusal says so — the size, the region, and that the quota
// is not granted — and never carries AWS's own message.
func TestAQuotaRefusalSaysTheQuotaIsNotGranted(t *testing.T) {
	f := hostedFake(t)
	f.Refuse("RunInstances", "VcpuLimitExceeded")

	_, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "gpu", InstanceType: "g5.xlarge"})
	want := "g5.xlarge in us-east-1 cannot launch yet: the hosted account's quota for this size is not granted (VcpuLimitExceeded)"
	if err == nil || err.Error() != want {
		t.Fatalf("quota refusal = %v, want %q", err, want)
	}

	f.Refuse("RunInstances", "InsufficientInstanceCapacity")
	_, err = LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "gpu", InstanceType: "p4d.24xlarge"})
	if err == nil || err.Error() != "no capacity for p4d.24xlarge in us-east-1 right now (InsufficientInstanceCapacity)" {
		t.Fatalf("capacity refusal = %v", err)
	}

	f.Refuse("RunInstances", "UnauthorizedOperation")
	_, err = LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "gpu", InstanceType: "g5.xlarge"})
	if err == nil || strings.Contains(err.Error(), "refused by the test") || !strings.Contains(err.Error(), "UnauthorizedOperation") {
		t.Fatalf("other refusal = %v, want the code and not the upstream message", err)
	}
	if len(f.Instances()) != 0 {
		t.Fatal("a refused launch left an instance")
	}
}

// The image is checked against the size before anything launches: an image that
// is not x86_64 cannot boot these types, and one whose root needs more disk than
// the price includes would cost more than it bills.
func TestTheImageMustFitTheSize(t *testing.T) {
	f := hostedFake(t)
	f.SetImage(ec2test.Image, "arm64", "/dev/sda1", 8)
	if _, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "b", InstanceType: "t3.medium"}); err == nil || !strings.Contains(err.Error(), "x86_64") {
		t.Fatalf("arm64 image = %v", err)
	}
	f.SetImage(ec2test.Image, "x86_64", "/dev/xvda", 80)
	if _, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "b", InstanceType: "t3.medium"}); err == nil || !strings.Contains(err.Error(), "80 GB") {
		t.Fatalf("oversized image = %v", err)
	}
	if n := len(f.Calls("RunInstances")); n != 0 {
		t.Fatalf("an image that does not fit launched %d times", n)
	}
}

// A retried launch returns the instance the first attempt started: the machine
// id is the launch's idempotency token.
func TestARetriedLaunchStartsOneInstance(t *testing.T) {
	f := hostedFake(t)
	api, _, err := hostedEC2(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o, _ := offerFor("t3.medium")
	l := launch{id: mintMachineID(), offer: o, name: "box", tags: map[string]string{orgTagKey: "acme"}}
	first, err := run(context.Background(), api, hostedConfig(), l)
	if err != nil {
		t.Fatal(err)
	}
	second, err := run(context.Background(), api, hostedConfig(), l)
	if err != nil {
		t.Fatal(err)
	}
	if *first.InstanceId != *second.InstanceId || len(f.Instances()) != 1 {
		t.Fatalf("a retry started a second instance: %s, %s", *first.InstanceId, *second.InstanceId)
	}
}

// ---- egress ----

// The whole path, as it runs: every EC2 call the SDK makes for a launch and a
// list is described to egress as an unsigned form POST for the account
// hanzo-compute at ec2.us-east-1.amazonaws.com, under compute's own token, and
// answered with AWS's XML. The pod's environment carries an AWS key, a session
// token, a role and endpoint overrides; none of them signs, moves or reaches
// anything.
func TestHostedEC2GoesThroughEgressUnsigned(t *testing.T) {
	f := hostedFake(t)
	if os.Getenv("AWS_ACCESS_KEY_ID") != ec2test.StaticKeyID || os.Getenv("AWS_ENDPOINT_URL_EC2") == "" {
		t.Fatal("the test must run with a key and an endpoint override in the environment")
	}
	if _, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "t3.medium"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := ListOrgMachines("acme", ""); err != nil {
		t.Fatalf("list: %v", err)
	}
	calls := f.Calls("")
	if refused := f.Calls("Refused"); len(refused) != 0 {
		t.Fatalf("egress refused %d calls: %s", len(refused), refused[0].Refused)
	}
	if len(calls) < 3 {
		t.Fatalf("only %d calls reached egress", len(calls))
	}
	for _, c := range calls {
		if c.Fetch.Host != ec2test.Endpoint || c.Fetch.Label != ec2test.Account || c.Bearer != "Bearer "+ec2test.Token {
			t.Fatalf("%s went to %q as %q under %q", c.Action, c.Fetch.Host, c.Fetch.Label, c.Bearer)
		}
		if strings.Contains(string(c.Fetch.Raw), ec2test.StaticKeyID) || strings.Contains(string(c.Fetch.Raw), "storage-") {
			t.Fatalf("%s carried the pod's key: %s", c.Action, c.Fetch.Raw)
		}
	}
}

// With no egress, hosted compute refuses by name. There is no direct call to
// fall back to: nothing here can sign one.
func TestHostedComputeRefusesWithoutEgress(t *testing.T) {
	f := hostedFake(t)
	RegisterCarrier(nil)

	_, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "t3.medium"})
	if !errors.Is(err, errNoEgress) || !strings.Contains(err.Error(), "hosted compute needs egress") {
		t.Fatalf("launch without egress = %v", err)
	}
	if _, err := ListOrgMachines("acme", ""); !errors.Is(err, errNoEgress) {
		t.Fatalf("list without egress = %v", err)
	}
	if err := ComputeReachable(context.Background()); !errors.Is(err, errNoEgress) {
		t.Fatalf("reachable without egress = %v — the sweep would claim an hour it cannot read", err)
	}
	if n := len(f.Calls("")); n != 0 {
		t.Fatalf("a refused call reached egress %d times", n)
	}
}

// Nothing this process builds for EC2 can sign: the client's only credential is
// anonymous, whatever the environment offers.
func TestNothingInComputeCanSignAnAWSRequest(t *testing.T) {
	hostedFake(t)
	api, region, err := hostedEC2(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if region != ec2test.Region {
		t.Fatalf("region = %q — the environment's AWS_REGION moved it", region)
	}
	// The SDK records anonymous credentials as no provider at all, and a client
	// with none signs nothing.
	if api.Options().Credentials != nil {
		t.Fatalf("the EC2 client holds credentials: %T", api.Options().Credentials)
	}
	if api.Options().BaseEndpoint != nil {
		t.Fatalf("the EC2 client was pointed at %q", *api.Options().BaseEndpoint)
	}
}

// Egress refusing — a role it cannot assume, an account with no descriptor —
// is what the caller hears as such, and the account reads as unreachable, so
// the hourly sweep claims no hour.
func TestAnEgressRefusalIsSaidAndReadAsUnreachable(t *testing.T) {
	f := hostedFake(t)
	f.Deny("egress: aws: the account's role was not assumed (AccessDenied)")

	_, err := ListOrgMachines("acme", "")
	if err == nil || !errors.Is(err, spend.ErrRefused) {
		t.Fatalf("list refused by egress = %v, want spend.ErrRefused", err)
	}
	if err := ComputeReachable(context.Background()); err == nil {
		t.Fatal("an account egress refuses read as reachable")
	}
	if n := len(f.Calls("Refused")); n != 2 {
		t.Fatalf("egress was asked %d times for two operations — a refusal was retried", n)
	}
}

// A tenant's own account is never carried under compute's identity: the
// carrier spends as compute, whose org is the platform's, so a row named after a
// platform account would spend the platform's. The carrier is not even asked,
// for any cloud. The platform's own accounts are carried, AWS anonymous.
func TestATenantsOwnAccountIsNeverCarried(t *testing.T) {
	var asked int
	RegisterCarrier(func(Credential) (*http.Client, error) { asked++; return &http.Client{}, nil })
	t.Cleanup(func() { RegisterCarrier(nil) })

	for _, provider := range []string{"AWS", "Hetzner"} {
		_, err := NewMachineClient(Credential{Provider: provider, Name: "hanzo-compute", Tenant: "mallory", Region: "us-east-1"})
		if !errors.Is(err, ErrTenantNotCarried) {
			t.Errorf("mallory's %s row under a carrier = %v, want ErrTenantNotCarried", provider, err)
		}
	}
	if asked != 0 {
		t.Fatalf("the carrier was asked %d times for a tenant's own account", asked)
	}

	client, err := NewMachineClient(Credential{Provider: "AWS", Name: "platform-aws", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("a platform AWS account was refused: %v", err)
	}
	carried, ok := client.(MachineAwsClient)
	if !ok || carried.Client.Options().Credentials != nil {
		t.Fatalf("a carried AWS client is %T and holds credentials", client)
	}
}

// The hourly sweep, end to end: every running hosted machine in the account is
// debited one hour at its catalog price to the org its own tag names, under the
// hour's meter id; a machine launched this hour is left to its launch debit.
func TestTheHourlySweepBillsEachOrgItsRunningMachines(t *testing.T) {
	f := hostedFake(t)
	var mu sync.Mutex
	var debits []commercetest.Usage
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage"):
			mu.Lock()
			debits = append(debits, commercetest.Read(r))
			mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_, _ = w.Write([]byte(`{"available":100000,"currency":"usd"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	earlier := time.Now().Add(-3 * time.Hour)
	tags := func(org, id string) map[string]string {
		return map[string]string{orgTagKey: org, machineTagKey: id, managedByKey: managedBy}
	}
	f.Add(ec2test.Instance{Type: "g5.xlarge", State: "running", LaunchTime: earlier, Tags: tags("acme", "m-44444444444444444444")})
	f.Add(ec2test.Instance{Type: "t3.medium", State: "running", LaunchTime: earlier, Tags: tags("beta", "m-55555555555555555555")})
	f.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: tags("beta", "m-66666666666666666666")}) // launched this hour
	f.Add(ec2test.Instance{Type: "t3.medium", State: "stopped", LaunchTime: earlier, Tags: tags("acme", "m-77777777777777777777")})
	// Every hour before this one is billed, and the machine launched this hour
	// had its launch debit land.
	for _, id := range []string{"m-44444444444444444444", "m-55555555555555555555", "m-77777777777777777777"} {
		MarkBilled(id, time.Now().Add(-time.Hour))
	}
	MarkBilled("m-66666666666666666666", time.Now())

	MeterRunningMachines(context.Background(), time.Now())

	mu.Lock()
	defer mu.Unlock()
	got := map[string]commercetest.Usage{}
	for _, d := range debits {
		got[d.ID] = d
	}
	now := time.Now().Truncate(time.Hour).Add(5 * time.Minute)
	a, b := got[MeterID("m-44444444444444444444", now)], got[MeterID("m-55555555555555555555", now)]
	disk := got[DiskMeterID("m-77777777777777777777", now)]
	if len(debits) != 3 || a.Org != "acme" || a.Cents() != 138 || a.Model != "g5.xlarge" || b.Org != "beta" || b.Cents() != 7 {
		t.Fatalf("sweep debits = %+v, want acme 138c for g5.xlarge and beta 7c for t3.medium", debits)
	}
	// A stopped machine pays its disk: 50 GB of gp3 is 1c an hour.
	if disk.Org != "acme" || disk.Cents() != 1 || disk.Model != "t3.medium/stopped" {
		t.Fatalf("the stopped machine's disk debit = %+v, want acme 1c", disk)
	}
}

// A stopped machine owes its disk for every hour after the last one billed,
// running or stopped, and once each; a machine started again owes its running
// hours from its start, not its stopped ones.
func TestAStoppedMachinePaysItsDiskOnce(t *testing.T) {
	recs, mu := fakeCommerce(t)
	seedCatalog(t, offer{slug: "s", listMicros: 37_500 - ipv4MicrosPerHour, diskGB: 200})

	at := func(h int) time.Time { return time.Date(2026, 7, 2, h, 10, 0, 0, time.UTC) }
	MarkBilled("rest", at(10)) // the launch's debit landed
	running := &Machine{Id: "rest", Size: "s", Tag: "hanzo-org:acme", State: "Running", CreatedTime: at(10).Format(time.RFC3339)}
	meterMachines(context.Background(), []*Machine{running}, at(11)) // running 11
	stopped := &Machine{Id: "rest", Size: "s", Tag: "hanzo-org:acme", State: "Stopped", CreatedTime: at(10).Format(time.RFC3339)}
	meterMachines(context.Background(), []*Machine{stopped}, at(14)) // stopped 12, 13, 14
	meterMachines(context.Background(), []*Machine{stopped}, at(14)) // nothing more
	if id := startCharge("rest", at(15)); id != "compute-rest-2026070215" {
		t.Fatalf("the start charges %q", id)
	}
	MarkBilled("rest", at(15)) // and the start's
	running.CreatedTime = at(15).Format(time.RFC3339)
	meterMachines(context.Background(), []*Machine{running}, at(16)) // running 16

	mu.Lock()
	defer mu.Unlock()
	var got []string
	for _, r := range *recs {
		got = append(got, r.usage.ID+"="+r.usage.Amount.Decimal)
	}
	want := []string{
		"compute-rest-2026070211=0.08",
		"compute-rest-disk-2026070212=0.03", "compute-rest-disk-2026070213=0.03", "compute-rest-disk-2026070214=0.03",
		"compute-rest-2026070216=0.08",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("debits = %v, want %v", got, want)
	}
}

// A list asks for pages small enough to cross egress: fifty instances.
func TestAListAsksForSmallPages(t *testing.T) {
	f := hostedFake(t)
	if _, err := ListOrgMachines("acme", ""); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls("DescribeInstances")
	if len(calls) == 0 {
		t.Fatal("no list reached egress")
	}
	for _, c := range calls {
		if n := c.Form.Get("MaxResults"); n != "50" {
			t.Fatalf("a list asked for %s instances a page", n)
		}
	}
}

// A volume is built from the same Credential a machine is, so a tenant's own row
// is refused before the carrier for it too.
func TestATenantsOwnAccountIsRefusedForEveryResource(t *testing.T) {
	var asked int
	RegisterCarrier(func(Credential) (*http.Client, error) { asked++; return &http.Client{}, nil })
	t.Cleanup(func() { RegisterCarrier(nil) })
	tenant := Credential{Provider: "Hetzner", Name: "hz", Tenant: "mallory", Region: "fsn1"}

	if _, err := NewVolumeClient(tenant); !errors.Is(err, ErrTenantNotCarried) {
		t.Errorf("volume = %v", err)
	}
	if asked != 0 {
		t.Fatalf("the carrier was asked %d times for a tenant's own account", asked)
	}
}

// No provider row reaches the hosted compute account, the SuperAdmin org's
// included: AWS under hostedLabel is hosted compute's alone, reached through
// carried. The platform's other rows are carried.
func TestNoRowIsTheHostedAccount(t *testing.T) {
	f := hostedFake(t)
	if _, err := NewMachineClient(Credential{Provider: "AWS", Name: hostedLabel, Region: ec2test.Region}); !errors.Is(err, ErrHostedNotARow) {
		t.Fatalf("a platform row named %s = %v, want ErrHostedNotARow", hostedLabel, err)
	}
	if _, err := NewVolumeClient(Credential{Provider: "AWS", Name: hostedLabel, Region: ec2test.Region}); !errors.Is(err, ErrHostedNotARow) {
		t.Fatalf("a platform volume row named %s = %v", hostedLabel, err)
	}
	if n := len(f.Calls("")); n != 0 {
		t.Fatalf("a row reached the hosted account %d times", n)
	}
	if _, err := ListOrgMachines("acme", ""); err != nil {
		t.Fatalf("hosted compute itself was refused: %v", err)
	}
	if _, err := NewMachineClient(Credential{Provider: "AWS", Name: "platform-aws", Region: ec2test.Region}); err != nil {
		t.Fatalf("another platform AWS row was refused: %v", err)
	}
	if !IsSuperAdmin("admin") || IsSuperAdmin("hanzo") || IsSuperAdmin("") || IsSuperAdmin("Admin") {
		t.Fatal("IsSuperAdmin is not exactly owner == \"admin\"")
	}
}

// NetworkOut end to end: the sweep reads each running machine's last settled
// hour from CloudWatch through egress, charges nothing for it, and stops a
// machine whose org cannot cover the transfer that rate could run up in an hour.
func TestNetworkOutIsReadThroughEgressAndStopsAMachineNeverCharges(t *testing.T) {
	f := hostedFake(t)
	var mu sync.Mutex
	var debits []string
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage"):
			mu.Lock()
			debits = append(debits, commercetest.Read(r).ID)
			mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_, _ = w.Write([]byte(`{"available":100000,"currency":"usd"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	now := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	h := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Hour) }
	heavy := f.Add(ec2test.Instance{Type: "t3.medium", State: "running", LaunchTime: h(4),
		Tags: map[string]string{orgTagKey: "acme", machineTagKey: "m-99999999999999999999", managedByKey: managedBy}})
	quiet := f.Add(ec2test.Instance{Type: "t3.medium", State: "running", LaunchTime: h(4),
		Tags: map[string]string{orgTagKey: "beta", machineTagKey: "m-88888888888888888888", managedByKey: managedBy}})
	MarkBilled("m-99999999999999999999", h(1))
	MarkBilled("m-88888888888888888888", h(1))
	f.Send(heavy.ID, h(3), 1<<30)  // settled, and not the last settled hour
	f.Send(heavy.ID, h(2), 10<<40) // the last settled hour: 10 TiB, $1,228.80 an hour
	f.Send(heavy.ID, h(1), 1<<30)  // not settled
	f.Send(quiet.ID, h(2), 1<<30)  // 12 cents an hour
	MeterRunningMachines(context.Background(), now)

	mu.Lock()
	got := append([]string(nil), debits...)
	mu.Unlock()
	if !slices.Equal(got, []string{MeterID("m-88888888888888888888", now)}) {
		t.Fatalf("debits = %v: the quiet machine's hour, and no transfer", got)
	}
	if stops := f.Calls("StopInstances"); len(stops) != 1 || stops[0].Form.Get("InstanceId.1") != heavy.ID {
		t.Fatalf("stops = %+v, want the heavy machine alone", stops)
	}
	if refused := f.Calls("Refused"); len(refused) != 0 {
		t.Fatalf("egress refused: %s", refused[0].Refused)
	}
	reads := f.Calls("GetMetricData")
	if len(reads) != 1 || reads[0].Fetch.Host != ec2test.Monitoring {
		t.Fatalf("CloudWatch reads = %+v", reads)
	}
	if from, to := reads[0].Form.Get("StartTime"), reads[0].Form.Get("EndTime"); from != h(2).Truncate(time.Hour).Format(time.RFC3339) || to != h(1).Truncate(time.Hour).Format(time.RFC3339) {
		t.Fatalf("CloudWatch was asked for [%s, %s), want the last settled hour", from, to)
	}
}

// A NetworkOut read pages by MaxDatapoints and adds up every page; a query
// CloudWatch could not count fails the read rather than reading as nothing sent.
func TestNetworkOutPagesAndRefusesAnUncountedQuery(t *testing.T) {
	f := hostedFake(t)
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * 30 * time.Hour)
	var instances []string
	for i := 0; i < 7; i++ {
		inst := f.Add(ec2test.Instance{Type: "t3.medium", State: "running", LaunchTime: from,
			Tags: map[string]string{orgTagKey: "acme", machineTagKey: fmt.Sprintf("m-%020d", i), managedByKey: managedBy}})
		instances = append(instances, inst.ID)
		for h := from; h.Before(to); h = h.Add(time.Hour) {
			f.Send(inst.ID, h, int64(i+1))
		}
	}
	sent, err := readOutbound(context.Background(), instances, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range instances {
		if n := len(sent[id]); n != 720 {
			t.Fatalf("%s: %d hours read, want 720", id, n)
		}
		if sent[id][hourOf(to.Add(-time.Hour))] != int64(i+1) {
			t.Fatalf("%s: the last hour reads %d", id, sent[id][hourOf(to.Add(-time.Hour))])
		}
	}
	if pages := len(f.Calls("GetMetricData")); pages != 3 {
		t.Fatalf("7 x 720 datapoints came in %d pages, want 3 of at most %d", pages, pageDatapoints)
	}
	f.Uncounted(instances[3], "InternalError")
	if _, err := readOutbound(context.Background(), instances, from, to); err == nil || !strings.Contains(err.Error(), "InternalError") {
		t.Fatalf("an uncounted query read as %v", err)
	}
}

// A GetMetricData answer longer than mostAnswer is refused, never parsed short.
func TestAnOversizedCloudWatchAnswerIsRefused(t *testing.T) {
	big := strings.Repeat(" ", mostAnswer+1)
	hc := &http.Client{Transport: answer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(big)), Header: http.Header{}}, nil
	})}
	at := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if _, _, err := metricPage(context.Background(), hc, "us-east-1", []string{"i-1"}, at, at.Add(time.Hour), ""); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a %d-byte answer read as %v", len(big), err)
	}
}

// answer is an http.RoundTripper that is one function.
type answer func(*http.Request) (*http.Response, error)

func (a answer) RoundTrip(r *http.Request) (*http.Response, error) { return a(r) }
