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

// These tests drive the hosted account through the real EC2 SDK client, against
// ec2test's fake account: every call is serialized, signed and sent exactly as it
// would be to AWS, and the assertions read what arrived.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/hanzoai/iamsdk/v2/iamsdk"

	"github.com/hanzoai/compute/service/commercetest"
	"github.com/hanzoai/compute/service/ec2test"
)

// hostedFake points the hosted account at a fresh fake account for one test.
func hostedFake(t *testing.T) *ec2test.Fake {
	t.Helper()
	f := ec2test.Serve(t)
	forgetHostedClient()
	t.Cleanup(forgetHostedClient)
	return f
}

// forgetHostedClient drops the cached client, which is bound to the endpoint it
// was built against.
func forgetHostedClient() {
	hosted.Lock()
	defer hosted.Unlock()
	hosted.client, hosted.signer = nil, signer{}
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
	if len(ids) != 2 || ids["m-11111111111111111111"] != "acme" || ids["m-22222222222222222222"] != "beta" {
		t.Fatalf("metered = %v, want the two running hosted machines with their orgs", ids)
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

// ---- credentials ----

// The whole chain, as it runs: the IAM app's client credential, form-encoded
// under Basic, buys a Hanzo IAM token; the token is the web identity STS takes,
// unsigned, for the configured role; and every EC2 call is signed with the key
// that exchange issued. The static key in the environment signs nothing, and
// nothing asks the instance metadata service.
func TestEC2IsSignedWithTheAssumedRole(t *testing.T) {
	f := hostedFake(t)
	if os.Getenv("AWS_ACCESS_KEY_ID") != ec2test.StaticKeyID {
		t.Fatal("the test must run with a static key in the environment")
	}
	if _, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "t3.medium"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := ListOrgMachines("acme", ""); err != nil {
		t.Fatalf("list: %v", err)
	}

	mints := f.Calls("IAMToken")
	if len(mints) != 1 || mints[0].Client != ec2test.ClientID || mints[0].Form.Get("grant_type") != "client_credentials" {
		t.Fatalf("IAM token requests = %+v, want one client_credentials grant for %s", mints, ec2test.ClientID)
	}
	assumed := f.Calls("AssumeRoleWithWebIdentity")
	if len(assumed) != 1 || assumed[0].KeyID != "" {
		t.Fatalf("role exchanges = %+v, want one, unsigned", assumed)
	}
	if got := assumed[0].Form.Get("RoleArn"); got != ec2test.RoleARN {
		t.Fatalf("assumed %q, want %q", got, ec2test.RoleARN)
	}
	if got := assumed[0].Form.Get("WebIdentityToken"); !strings.HasPrefix(got, "iam-token-") {
		t.Fatalf("the web identity was %q, not the IAM token", got)
	}
	if got := assumed[0].Form.Get("RoleSessionName"); got != managedBy {
		t.Fatalf("session name %q, want %q", got, managedBy)
	}

	var ec2Calls int
	for _, c := range f.Calls("") {
		switch c.Action {
		case "IAMToken", "AssumeRoleWithWebIdentity":
		case "IMDS":
			t.Fatal("the instance metadata service was asked for credentials")
		default:
			ec2Calls++
			if !strings.HasPrefix(c.KeyID, ec2test.RoleKeyPrefix) || !f.IsRoleKey(c.KeyID) {
				t.Fatalf("%s was signed with %q, not the assumed role's key", c.Action, c.KeyID)
			}
		}
	}
	if ec2Calls < 3 {
		t.Fatalf("only %d EC2 calls: the launch and the list did not both reach the account", ec2Calls)
	}
}

// The role's credentials are replaced before they expire, and so is the IAM
// token they are bought with: a key that expires inside the refresh window is
// never used again, and a token IAM says is about to lapse is minted anew. With
// long lifetimes, nothing is fetched twice.
func TestTheRoleIsRefreshedBeforeItExpires(t *testing.T) {
	f := hostedFake(t)
	for range 3 {
		if _, err := ListOrgMachines("acme", ""); err != nil {
			t.Fatal(err)
		}
	}
	if m, a := len(f.Calls("IAMToken")), len(f.Calls("AssumeRoleWithWebIdentity")); m != 1 || a != 1 {
		t.Fatalf("long-lived credentials fetched %d tokens and %d roles for three calls, want 1 and 1", m, a)
	}

	f.Reset()
	forgetHostedClient()
	// The role key lives two minutes, inside the five-minute refresh window; the
	// token lives thirty seconds, inside Identity's one-minute margin.
	f.Lifetimes(30*time.Second, 2*time.Minute)
	for range 3 {
		if _, err := ListOrgMachines("acme", ""); err != nil {
			t.Fatal(err)
		}
	}
	assumed := f.Calls("AssumeRoleWithWebIdentity")
	if len(assumed) != 3 || len(f.Calls("IAMToken")) != 3 {
		t.Fatalf("short-lived credentials: %d role exchanges and %d token mints for three calls, want 3 and 3",
			len(assumed), len(f.Calls("IAMToken")))
	}
	seen := map[string]bool{}
	for _, a := range assumed {
		tok := a.Form.Get("WebIdentityToken")
		if seen[tok] {
			t.Fatalf("a token about to lapse was presented twice: %s", tok)
		}
		seen[tok] = true
	}
	keys := map[string]bool{}
	for _, c := range f.Calls("DescribeInstances") {
		keys[c.KeyID] = true
	}
	if len(keys) != 3 {
		t.Fatalf("three calls were signed with %d keys, want a fresh key each", len(keys))
	}
}

// A missing role, client id or client secret refuses every call by name,
// before anything is asked of IAM, STS or EC2 — and the hourly sweep reads it as
// an account it cannot reach, so it claims no hour.
func TestAMissingRoleOrClientRefuses(t *testing.T) {
	for _, key := range []string{keyRoleARN, keyIAMClientID, keyIAMClientSecret} {
		t.Run(key, func(t *testing.T) {
			f := hostedFake(t)
			t.Setenv(key, "")
			name := key + " (" + envOf[key] + ")"

			_, err := LaunchOrgMachine(context.Background(), "acme", "", &CreateMachineSpec{Name: "box", InstanceType: "t3.medium"})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("launch = %v, want a refusal naming %s", err, name)
			}
			if _, err := ListOrgMachines("acme", ""); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("list = %v, want a refusal naming %s", err, name)
			}
			if err := ComputeReachable(context.Background()); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("reachable = %v, want a refusal naming %s", err, name)
			}
			if n := len(f.Calls("")); n != 0 {
				t.Fatalf("a refused call reached the fake %d times", n)
			}
		})
	}
}

// A client credential IAM refuses buys no role, and nothing reaches EC2. The
// caller is told the role could not be assumed, not that the cloud is down.
func TestARefusedClientAssumesNothing(t *testing.T) {
	f := hostedFake(t)
	t.Setenv(keyIAMClientSecret, "stale")

	_, err := ListOrgMachines("acme", "")
	if err == nil || !errors.Is(err, errRole) {
		t.Fatalf("list with a refused client = %v, want the role refusal", err)
	}
	if len(f.Calls("AssumeRoleWithWebIdentity")) != 0 || len(f.Calls("DescribeInstances")) != 0 {
		t.Fatal("a refused client reached STS or EC2")
	}
	if err := ComputeReachable(context.Background()); err == nil {
		t.Fatal("an account whose role cannot be assumed read as reachable")
	}
}

// The chain holds no key and takes none from the environment: whatever
// AWS_ACCESS_KEY_ID, AWS_ROLE_ARN or AWS_WEB_IDENTITY_TOKEN_FILE say, the only
// provider is the web-identity exchange for the configured role.
func TestTheRoleChainHasNoOtherStep(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", ec2test.StaticKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "storage-secret")
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::000000000000:role/somebody-else")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/var/run/secrets/token")
	cfg, err := awsConfig(context.Background(), signer{Region: ec2test.Region, RoleARN: ec2test.RoleARN,
		ClientID: ec2test.ClientID, ClientSecret: ec2test.ClientSecret, IAM: hanzoIAM})
	if err != nil {
		t.Fatal(err)
	}
	cache, ok := cfg.Credentials.(*aws.CredentialsCache)
	if !ok {
		t.Fatalf("credentials are %T, want the SDK cache", cfg.Credentials)
	}
	if !cache.IsCredentialsProvider(assumed{}) {
		t.Fatal("the cache does not hold the role provider")
	}
	role, ok := roleCredentials(cfg, hostedConfig().signer).(assumed)
	if !ok {
		t.Fatal("the role chain is not the assumed role")
	}
	if _, ok := role.CredentialsProvider.(*stscreds.WebIdentityRoleProvider); !ok {
		t.Fatalf("the role provider is %T, want the web-identity exchange", role.CredentialsProvider)
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
		if strings.HasSuffix(r.URL.Path, "/usage") {
			mu.Lock()
			debits = append(debits, commercetest.Read(r))
			mu.Unlock()
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

	MeterRunningMachines(context.Background())

	mu.Lock()
	defer mu.Unlock()
	got := map[string]commercetest.Usage{}
	for _, d := range debits {
		got[d.ID] = d
	}
	now := time.Now()
	a, b := got[MeterID("m-44444444444444444444", now)], got[MeterID("m-55555555555555555555", now)]
	if len(debits) != 2 || a.Org != "acme" || a.Cents() != 138 || a.Model != "g5.xlarge" || b.Org != "beta" || b.Cents() != 7 {
		t.Fatalf("sweep debits = %+v, want acme 138c for g5.xlarge and beta 7c for t3.medium", debits)
	}
}
