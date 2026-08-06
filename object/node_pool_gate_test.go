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

package object

import (
	"testing"

	"github.com/hanzoai/visor/service"
)

// An autoscaling pool grows without asking. MinNodes/MaxNodes/AutoScale are
// forwarded to the upstream, which adds nodes whenever the scheduler wants them
// — no request reaches visor, so the money gate never runs on the growth. An org
// authorized for one node could therefore end up running sixteen.
//
// The gate authorizes what the pool is ALLOWED to become, not what it starts as.
func TestAuthorizedNodesCoversTheAutoscaleCeiling(t *testing.T) {
	for name, tc := range map[string]struct {
		spec service.CreateNodePoolSpec
		want int
	}{
		"a fixed pool is its own count":         {service.CreateNodePoolSpec{Count: 4}, 4},
		"a fixed pool floors at one":            {service.CreateNodePoolSpec{Count: 0}, 1},
		"a fixed pool ignores stray bounds":     {service.CreateNodePoolSpec{Count: 2, MaxNodes: 64}, 2},
		"an autoscaling pool takes its ceiling": {service.CreateNodePoolSpec{Count: 1, MinNodes: 1, MaxNodes: 16, AutoScale: true}, 16},
		"a ceiling below the count loses":       {service.CreateNodePoolSpec{Count: 8, MinNodes: 1, MaxNodes: 4, AutoScale: true}, 8},
		"a floor above the count wins":          {service.CreateNodePoolSpec{Count: 1, MinNodes: 6, AutoScale: true}, 6},
		"an autoscaling pool still floors":      {service.CreateNodePoolSpec{Count: 0, AutoScale: true}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := authorizedNodes(&tc.spec); got != tc.want {
				t.Fatalf("authorizedNodes(%+v) = %d, want %d", tc.spec, got, tc.want)
			}
		})
	}
}

// The count asked of the upstream is the count the org is authorized for. A
// non-positive count used to reach DigitalOcean as-is while the gate priced a
// floor of one.
func TestPoolNodesFloorsWhatIsProvisioned(t *testing.T) {
	for name, tc := range map[string]struct {
		count, want int
	}{"zero floors": {0, 1}, "negative floors": {-3, 1}, "one is one": {1, 1}, "four is four": {4, 4}} {
		t.Run(name, func(t *testing.T) {
			spec := &service.CreateNodePoolSpec{Count: tc.count}
			if got := poolNodes(spec); got != tc.want {
				t.Fatalf("poolNodes(count=%d) = %d, want %d", tc.count, got, tc.want)
			}
		})
	}
}
