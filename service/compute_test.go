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
	"testing"

	"github.com/digitalocean/godo"
)

// TestBuildDropletTags_AttributionUnforgeable proves a tenant cannot forge,
// strip, or DUPLICATE the hanzo-org attribution tag through the launch body of a
// DigitalOcean provider: the authoritative org (set under the reserved map key)
// is the ONLY hanzo-org token emitted, and any client tag that could
// smuggle a second attribution token via the meter's "," / ":" separators is
// dropped — so orgFromTag reads back exactly the server-resolved org.
func TestBuildDropletTags_AttributionUnforgeable(t *testing.T) {
	// Simulates the state after the launch set the reserved key:
	// client tried to smuggle a second attribution token and to override the key.
	spec := &CreateMachineSpec{Tags: map[string]string{
		orgTagKey:  "acme",               // authoritative (the launch overwrote any client value)
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

// A DigitalOcean call with no timeout does not fail — it waits for as long as the
// socket stays open. That matters here more than usual because a provision waits
// UNDER the org's provisioning hold, so one hung api.digitalocean.com wedges that
// org's every subsequent provision behind a mutex nobody will release.
//
// Every DO surface builds through the one constructor, so the bound is one
// statement rather than four that have to agree.
func TestEveryDigitalOceanClientIsBounded(t *testing.T) {
	machine, err := newMachineDigitalOceanClient("", "tok", "nyc3", nil)
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
		if c.HTTPClient.Timeout != providerTimeout {
			t.Fatalf("%s: DigitalOcean calls are unbounded (timeout=%v), so a hung upstream holds the org's provisioning lease forever",
				name, c.HTTPClient.Timeout)
		}
	}
}
