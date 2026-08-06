// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
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

import (
	"math"
	"testing"

	"github.com/digitalocean/godo"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestHanzoPriceMarkup(t *testing.T) {
	// Standard sizes use the base markup; GPU sizes the gentler GPU markup.
	if got := HanzoPrice(48.0, false); !almost(got, 64.0) {
		t.Fatalf("base monthly markup: got %v want 64.0", got)
	}
	if got := HanzoPrice(0.00744, false); !almost(got, 0.00992) {
		t.Fatalf("base hourly markup: got %v want 0.00992", got)
	}
	if got := HanzoPrice(3.0, true); !almost(got, 4.0) {
		t.Fatalf("gpu markup: got %v want 4.0", got)
	}
	// Resale price must always exceed wholesale — the margin invariant.
	for _, do := range []float64{0.007, 0.5, 48, 3200} {
		if HanzoPrice(do, false) <= do || HanzoPrice(do, true) <= do {
			t.Fatalf("resale price must exceed wholesale for %v", do)
		}
	}
}

func TestIsGPUSize(t *testing.T) {
	cases := []struct {
		size godo.Size
		want bool
	}{
		{godo.Size{Slug: "s-2vcpu-4gb"}, false},
		{godo.Size{Slug: "gpu-h100x1-80gb"}, true},
		{godo.Size{Slug: "g-2vcpu-8gb", GPUInfo: &godo.GPUInfo{Count: 1, Model: "H100"}}, true},
	}
	for _, c := range cases {
		if got := isGPUSize(c.size); got != c.want {
			t.Fatalf("isGPUSize(%q)=%v want %v", c.size.Slug, got, c.want)
		}
	}
}

func TestSizeInfoFromDO_GPU(t *testing.T) {
	s := godo.Size{
		Slug:         "gpu-h100x1-80gb",
		Vcpus:        20,
		Memory:       240 * 1024,
		Disk:         720,
		Available:    true,
		Regions:      []string{"nyc2", "tor1"},
		PriceHourly:  3.0,
		PriceMonthly: 2000.0,
		GPUInfo:      &godo.GPUInfo{Count: 1, Model: "H100", VRAM: &godo.VRAM{Amount: 80, Unit: "GiB"}},
	}
	si := sizeInfoFromDO(s)
	if si.GPU == nil || si.GPU.Model != "H100" || si.GPU.Count != 1 || si.GPU.Vram != 80 {
		t.Fatalf("gpu spec not mapped: %+v", si.GPU)
	}
	if !almost(si.PriceHourly, HanzoPrice(3.0, true)) {
		t.Fatalf("gpu hourly resale: got %v", si.PriceHourly)
	}
	if !almost(si.PriceMonthly, HanzoPrice(2000.0, true)) {
		t.Fatalf("gpu monthly resale: got %v", si.PriceMonthly)
	}
}

// TestBuildDropletTags_AttributionUnforgeable proves a tenant cannot forge,
// strip, or DUPLICATE the hanzo-org billing-attribution tag through the launch
// body: the authoritative org (injected by LaunchOrgMachine under the reserved
// map key) is the ONLY hanzo-org token emitted, and any client tag that could
// smuggle a second attribution token via the meter's "," / ":" separators is
// dropped — so orgFromTag reads back exactly the server-resolved org.
func TestBuildDropletTags_AttributionUnforgeable(t *testing.T) {
	// Simulates the post-LaunchOrgMachine state: server has set the reserved key,
	// client tried to smuggle a second attribution token and to override the key.
	spec := &CreateMachineSpec{Tags: map[string]string{
		orgTagKey:  "acme",               // authoritative (LaunchOrgMachine overwrote any client value)
		"note":     "y,hanzo-org:victim", // comma-smuggle a 2nd attribution token -> must be DROPPED
		"role":     "svc:admin",          // colon-smuggle a fake key:value -> must be DROPPED
		"team":     "platform",           // clean -> kept
		"env:PORT": "8080",               // env: prefix -> not a DO tag (cloud-init only)
	}}
	tags := buildDropletTags(spec)

	joined := ""
	orgCount := 0
	for _, tg := range tags {
		joined += tg + ","
		if len(tg) >= len(orgTagKey)+1 && tg[:len(orgTagKey)+1] == orgTagKey+":" {
			orgCount++
		}
	}
	if orgCount != 1 {
		t.Fatalf("expected exactly one hanzo-org token, got %d in %v", orgCount, tags)
	}
	if got := orgFromTag(joined); got != "acme" {
		t.Fatalf("orgFromTag read back %q, want acme (attribution must be the server org) — tags=%v", got, tags)
	}
	for _, tg := range tags {
		if tg == "note:y,hanzo-org:victim" || tg == "role:svc:admin" || tg == "env:PORT:8080" {
			t.Fatalf("unsafe/smuggling tag was not dropped: %q in %v", tg, tags)
		}
		if tg == "team:platform" {
			// clean tag preserved — good
		}
	}
}

// display-name and os are also client-controlled; a smuggled attribution token
// there must not survive the read-back either.
func TestBuildDropletTags_DisplayNameOSCannotSmuggle(t *testing.T) {
	spec := &CreateMachineSpec{
		DisplayName: "box,hanzo-org:victim",
		OS:          "ubuntu:hanzo-org:victim",
		Tags:        map[string]string{orgTagKey: "acme"},
	}
	tags := buildDropletTags(spec)
	joined := ""
	for _, tg := range tags {
		joined += tg + ","
		if tg == "display-name:box,hanzo-org:victim" || tg == "os:ubuntu:hanzo-org:victim" {
			t.Fatalf("smuggling display-name/os tag emitted: %q", tg)
		}
	}
	if got := orgFromTag(joined); got != "acme" {
		t.Fatalf("orgFromTag = %q, want acme (display-name/os must not smuggle attribution)", got)
	}
}

func TestValidOrgSlug(t *testing.T) {
	ok := []string{"acme", "max-power", "hanzo", "a", "org123"}
	bad := []string{"", "  ", "acme,evil", "acme:evil", "has space", "hanzo-org:victim", "a\tb"}
	for _, s := range ok {
		if !validOrgSlug(s) {
			t.Fatalf("validOrgSlug(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validOrgSlug(s) {
			t.Fatalf("validOrgSlug(%q) = true, want false (billing key / tag must be a clean slug)", s)
		}
	}
}

// TestOrgIsolationPredicate proves the per-tenant isolation invariant: a droplet
// tagged for one org is visible ONLY to that org. This is the security boundary
// RED must verify end-to-end; here it is asserted at the data layer.
func TestOrgIsolationPredicate(t *testing.T) {
	if orgTag("maxpower") != "hanzo-org:maxpower" {
		t.Fatalf("orgTag format: got %q", orgTag("maxpower"))
	}
	d := &godo.Droplet{Tags: []string{"managed-by:hanzo-visor", "hanzo-org:maxpower"}}
	if !dropletHasOrgTag(d, "maxpower") {
		t.Fatal("owner org must see its own machine")
	}
	if dropletHasOrgTag(d, "hanzo") {
		t.Fatal("cross-tenant leak: another org must NOT see this machine")
	}
	if dropletHasOrgTag(&godo.Droplet{Tags: nil}, "maxpower") {
		t.Fatal("untagged droplet must not match any org")
	}
}

// ---- exactly one meter per node ----

// A cluster's worker droplets carry the cluster's tags, hanzo-org among them, so
// the droplet sweep can see the very same nodes the node-pool sweep bills as part
// of their pool. Two meters on one node is a double charge every hour, forever,
// and it is invisible from inside either sweep.
//
// The node-pool sweep is the meter of record for a cluster's nodes, so the
// machine meter must not admit a Kubernetes worker — whether or not DigitalOcean
// propagates the tag. That is what makes this a property of visor.
func TestBillableHouseDroplet_KubernetesWorkersAreNotOnTheMachineMeter(t *testing.T) {
	for name, tc := range map[string]struct {
		droplet godo.Droplet
		want    bool
	}{
		"a running resell droplet is billed": {
			godo.Droplet{Status: "active", Tags: []string{"managed-by:hanzo-visor", "hanzo-org:acme"}}, true},
		"a DOKS worker carrying the cluster's org tag is NOT": {
			godo.Droplet{Status: "active", Tags: []string{"k8s", "k8s-worker", "k8s:cl-1", "hanzo-org:acme"}}, false},
		"the bare k8s tag alone is enough to exclude it": {
			godo.Droplet{Status: "active", Tags: []string{"k8s", "hanzo-org:acme"}}, false},
		"so is k8s-worker alone": {
			godo.Droplet{Status: "active", Tags: []string{"k8s-worker", "hanzo-org:acme"}}, false},
		"a stopped resell droplet is not billed": {
			godo.Droplet{Status: "off", Tags: []string{"hanzo-org:acme"}}, false},
		"an untagged house droplet is never billed to a tenant": {
			godo.Droplet{Status: "active", Tags: []string{"managed-by:hanzo-visor"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := billableHouseDroplet(tc.droplet); got != tc.want {
				t.Fatalf("billableHouseDroplet(%v) = %v, want %v", tc.droplet.Tags, got, tc.want)
			}
		})
	}
}

// The exclusion must not become an opt-out. A tenant who could put the bare `k8s`
// tag on its own droplet would run it free forever — the guard would do the
// hiding for them.
//
// It cannot: every client tag reaches DigitalOcean as "key:value", so a customer
// asking for `k8s` gets `k8s:<something>`, which is not one of the bare automatic
// tags DigitalOcean applies. This is why the guard matches exactly and never on a
// prefix.
func TestBuildDropletTags_ACustomerCannotForgeTheKubernetesExclusion(t *testing.T) {
	spec := &CreateMachineSpec{Tags: map[string]string{
		orgTagKey:    "acme",
		"k8s":        "",    // bare-tag attempt
		"k8s-worker": "",    // bare-tag attempt
		"k8s2":       "k8s", // value-side attempt
	}}
	tags := buildDropletTags(spec)

	for _, tg := range tags {
		if kubernetesAutoTags[tg] {
			t.Fatalf("a client produced the bare exclusion tag %q — its droplet would run free: %v", tg, tags)
		}
	}
	// And the droplet those tags describe is still on the meter.
	if !billableHouseDroplet(godo.Droplet{Status: "active", Tags: tags}) {
		t.Fatalf("a customer escaped the machine meter with launch tags: %v", tags)
	}
}

// A DigitalOcean call with no timeout does not fail — it waits for as long as the
// socket stays open. That matters here more than usual because a provision waits
// UNDER the org's provisioning hold, so one hung api.digitalocean.com wedges that
// org's every subsequent provision behind a mutex nobody will release.
//
// Every DO surface builds through the one constructor, so the bound is one
// statement rather than four that have to agree.
func TestEveryDigitalOceanClientIsBounded(t *testing.T) {
	machine, err := newMachineDigitalOceanClient("", "tok", "nyc3")
	if err != nil {
		t.Fatalf("machine client: %v", err)
	}
	volume, err := newVolumeDigitalOceanClient("", "tok", "nyc3")
	if err != nil {
		t.Fatalf("volume client: %v", err)
	}
	doks, err := NewDOKSClient("tok", "cl-1")
	if err != nil {
		t.Fatalf("doks client: %v", err)
	}
	cost, err := newDOCostReader("", "tok")
	if err != nil {
		t.Fatalf("cost reader: %v", err)
	}

	for name, c := range map[string]*godo.Client{
		"droplets":   machine.Client,
		"volumes":    volume.Client,
		"kubernetes": doks.Client,
		"cost":       cost.(*doCostReader).client,
	} {
		if c.HTTPClient == nil {
			t.Fatalf("%s: no HTTP client at all", name)
		}
		if c.HTTPClient.Timeout != doAPITimeout {
			t.Fatalf("%s: DigitalOcean calls are unbounded (timeout=%v), so a hung upstream holds the org's provisioning lease forever",
				name, c.HTTPClient.Timeout)
		}
	}
}
