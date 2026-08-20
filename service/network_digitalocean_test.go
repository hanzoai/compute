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

// A load balancer with no rules is the obvious one, not a refusal. The provider
// rejects a balancer with an empty rule set, so a caller who omits them would be
// handed an error instead of the thing they asked for.
func TestNoForwardingRulesMeansPlainHTTP(t *testing.T) {
	got := toDOForwardingRules(nil)
	if len(got) != 1 {
		t.Fatalf("empty rules produced %d rules, want exactly one default", len(got))
	}
	r := got[0]
	if r.EntryProtocol != "http" || r.EntryPort != 80 || r.TargetProtocol != "http" || r.TargetPort != 80 {
		t.Errorf("default rule = %s/%d -> %s/%d, want http/80 -> http/80",
			r.EntryProtocol, r.EntryPort, r.TargetProtocol, r.TargetPort)
	}
}

// Stated rules pass through unchanged — the default must not leak into a caller
// who was explicit.
func TestStatedRulesArePassedThrough(t *testing.T) {
	got := toDOForwardingRules([]ForwardingRule{
		{EntryProtocol: "https", EntryPort: 443, TargetProtocol: "http", TargetPort: 8080},
	})
	if len(got) != 1 {
		t.Fatalf("one rule in, %d out", len(got))
	}
	if got[0].EntryPort != 443 || got[0].TargetPort != 8080 || got[0].EntryProtocol != "https" {
		t.Errorf("rule = %+v, want https/443 -> http/8080", got[0])
	}
}

// A DigitalOcean VPC carries no lifecycle field. Reporting "active" is the
// accurate model; reading a status off a field that does not exist would be an
// invented value.
func TestAVpcIsActiveBecauseItExists(t *testing.T) {
	v := getVpcFromDOVpc(&godo.VPC{ID: "abc", Name: "edge", RegionSlug: "nyc3", IPRange: "10.1.0.0/16"})
	if v.State != vpcStateActive {
		t.Errorf("state = %q, want %q", v.State, vpcStateActive)
	}
	if v.Id != "abc" || v.DisplayName != "edge" || v.Cidr != "10.1.0.0/16" || v.Region != "nyc3" {
		t.Errorf("mapped = %+v", v)
	}
}

// Targets is the count of backends actually attached — the number an operator
// can act on, never a configured capacity.
func TestTargetsCountsAttachedBackends(t *testing.T) {
	lb := getLoadBalancerFromDOLoadBalancer(&godo.LoadBalancer{
		ID: "lb1", Name: "edge", IP: "1.2.3.4", Status: "active",
		DropletIDs: []int{1, 2, 3},
		Region:     &godo.Region{Slug: "nyc3"},
	})
	if lb.Targets != 3 {
		t.Errorf("targets = %d, want 3", lb.Targets)
	}
	if lb.Region != "nyc3" || lb.Ip != "1.2.3.4" || lb.State != "active" {
		t.Errorf("mapped = %+v", lb)
	}
}

// A load balancer with no region must not panic — godo leaves the pointer nil
// while one is still provisioning.
func TestALoadBalancerWithNoRegionYet(t *testing.T) {
	lb := getLoadBalancerFromDOLoadBalancer(&godo.LoadBalancer{ID: "lb2", Status: "new"})
	if lb.Region != "" {
		t.Errorf("region = %q, want empty", lb.Region)
	}
}
