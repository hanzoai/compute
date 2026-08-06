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
	"fmt"
	"testing"
)

// TestNodePoolMachines pins the DOKS node -> fleet Machine mapping: one Machine
// per WORKER NODE (not per pool), keyed by the node's DropletID so it dedups
// against the live droplet list, sized by its pool, regioned + cluster-tagged by
// its cluster, and carrying the node's DO status. A node still provisioning (no
// DropletID) has no fleet identity and is skipped. IPs are honestly empty.
func TestNodePoolMachines(t *testing.T) {
	cluster := &KubernetesCluster{ID: "cl-1", Name: "prod", RegionSlug: "sfo3"}
	pools := []*NodePool{
		{
			Name: "default", Size: "s-4vcpu-8gb",
			Nodes: []NodeInfo{
				{ID: "n-1", Name: "prod-default-aaa", DropletID: "111", Status: "running"},
				{ID: "n-2", Name: "prod-default-bbb", DropletID: "222", Status: "running"},
				{ID: "n-3", Name: "prod-default-ccc", DropletID: "", Status: "provisioning"}, // skipped
			},
		},
		{
			Name: "gpu", Size: "gpu-h100x1-80gb",
			Nodes: []NodeInfo{
				{ID: "n-4", Name: "prod-gpu-ddd", DropletID: "333", Status: "running"},
			},
		},
	}

	got := nodePoolMachines(cluster, pools)
	if len(got) != 3 {
		t.Fatalf("want 3 machines (2 default + 1 gpu, provisioning node skipped), got %d: %+v", len(got), got)
	}

	byID := map[string]*Machine{}
	for _, m := range got {
		byID[m.Id] = m
	}
	for _, tc := range []struct{ id, name, size string }{
		{"111", "prod-default-aaa", "s-4vcpu-8gb"},
		{"222", "prod-default-bbb", "s-4vcpu-8gb"},
		{"333", "prod-gpu-ddd", "gpu-h100x1-80gb"},
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("node droplet %q missing from mapped machines: %+v", tc.id, got)
		}
		if m.Name != tc.name || m.Size != tc.size {
			t.Errorf("droplet %q: got name=%q size=%q, want name=%q size=%q", tc.id, m.Name, m.Size, tc.name, tc.size)
		}
		if m.Provider != "DigitalOcean" || m.Region != "sfo3" || m.State != "running" || m.Tag != "doks-cluster:prod" {
			t.Errorf("droplet %q: got provider=%q region=%q state=%q tag=%q, want DigitalOcean/sfo3/running/doks-cluster:prod",
				tc.id, m.Provider, m.Region, m.State, m.Tag)
		}
		if m.PublicIp != "" || m.PrivateIp != "" {
			t.Errorf("droplet %q: IPs must be empty (DOKS node API exposes none), got public=%q private=%q", tc.id, m.PublicIp, m.PrivateIp)
		}
	}

	// A cluster with no pools maps to no machines (no panic, honest empty).
	if got := nodePoolMachines(&KubernetesCluster{Name: "empty"}, nil); len(got) != 0 {
		t.Fatalf("empty cluster want 0 machines, got %d", len(got))
	}
}

// An autoscaling pool grows and shrinks without a request reaching visor, so
// nothing writes the new number down anywhere visor controls. The nodes the
// provider reports are the only ground truth, and every one of them is a droplet
// Hanzo is paying for — so the count that bills is the count that exists.
//
// The declared Count is the fallback for a pool returned without node detail: a
// missing list must never be read as "free".
func TestLiveNodesCountsTheNodesThatExist(t *testing.T) {
	nodes := func(n int) []NodeInfo {
		out := make([]NodeInfo, n)
		for i := range out {
			out[i] = NodeInfo{ID: fmt.Sprintf("n-%d", i), DropletID: fmt.Sprintf("%d", 100+i)}
		}
		return out
	}
	for name, tc := range map[string]struct {
		pool *NodePool
		want int
	}{
		"an autoscaled pool bills what it grew to": {&NodePool{Count: 1, Nodes: nodes(16)}, 16},
		"a shrunk pool bills what is left":         {&NodePool{Count: 16, Nodes: nodes(2)}, 2},
		"an agreeing pool is itself":               {&NodePool{Count: 4, Nodes: nodes(4)}, 4},
		"no node detail falls back to the count":   {&NodePool{Count: 3}, 3},
		"an empty pool costs nothing":              {&NodePool{Count: 0}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if got := liveNodes(tc.pool); got != tc.want {
				t.Fatalf("liveNodes(count=%d nodes=%d) = %d, want %d",
					tc.pool.Count, len(tc.pool.Nodes), got, tc.want)
			}
		})
	}
}
