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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// ---- the gate, end to end: a refusal reaches no upstream ----

// fakeCloudPool is the upstream, recording what it was ASKED for. A gate is only
// proven by an upstream that stays untouched when it refuses.
type fakeCloudPool struct {
	created *service.CreateNodePoolSpec
	updated *service.CreateNodePoolSpec
}

func (f *fakeCloudPool) CreateNodePool(spec *service.CreateNodePoolSpec) (*service.NodePool, error) {
	f.created = spec
	return &service.NodePool{ID: "p-new", Name: spec.Name, Size: spec.Size, Count: spec.Count,
		MinNodes: spec.MinNodes, MaxNodes: spec.MaxNodes, AutoScale: spec.AutoScale}, nil
}

func (f *fakeCloudPool) UpdateNodePool(poolID string, spec *service.CreateNodePoolSpec) (*service.NodePool, error) {
	f.updated = spec
	return &service.NodePool{ID: poolID, Name: spec.Name, Size: spec.Size, Count: spec.Count}, nil
}

// commerce stands up a fake commerce in one of two moods and reports what it was
// asked. reads counts balance checks, so "the gate did not even look" is
// distinguishable from "the gate looked and allowed".
type commerce struct {
	mu     sync.Mutex
	reads  int
	debits int
	amount int64
}

func commerceOf(t *testing.T, availableCents int64) *commerce {
	t.Helper()
	c := &commerce{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/balance"):
			c.reads++
			_ = json.NewEncoder(w).Encode(map[string]any{"available": availableCents, "currency": "usd"})
		case strings.HasSuffix(r.URL.Path, "/usage"):
			var u struct {
				Amount int64 `json:"amount"`
			}
			_ = json.NewDecoder(r.Body).Decode(&u)
			c.debits++
			c.amount += u.Amount
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"transactionId":"tx","type":"usage"}`))
		default:
			// Anything else is the scope-cap read, which fails open by contract:
			// an unrouted 404 here is a legitimate "no cap configured".
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMMERCE_URL", srv.URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	return c
}

func (c *commerce) state() (reads, debits int, amount int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, c.debits, c.amount
}

// rateH100 is the resale rate of a gpu-h100x8-640gb node-hour, in cents — what
// HourlyCents resolves from the catalog at the edge and hands the metered core.
const rateH100 = 3178

// A node pool is not created for an org that cannot pay for it. Remove the gate
// and the upstream receives the create anyway — which is the whole leak, and is
// what this asserts against.
func TestCreateNodePoolMeteredRefusedReachesNoUpstream(t *testing.T) {
	installBaseStore(t)
	c := commerceOf(t, 0) // broke

	cloud := &fakeCloudPool{}
	pool, err := createNodePoolMetered(cloud, rateH100, "acme", "", "do", "cl-1",
		&service.CreateNodePoolSpec{Name: "gpu", Size: "gpu-h100x8-640gb", Count: 4})

	if err == nil || pool != nil {
		t.Fatalf("an unfunded org must be refused, got pool=%+v err=%v", pool, err)
	}
	if cloud.created != nil {
		t.Fatalf("REFUSED BUT PROVISIONED: upstream received %+v", cloud.created)
	}
	reads, debits, _ := c.state()
	if reads == 0 {
		t.Fatal("the gate must consult the balance before provisioning")
	}
	if debits != 0 {
		t.Fatalf("a refused provision must bill nothing, got %d debits", debits)
	}
	if pools, _ := GetNodePools("acme"); len(pools) != 0 {
		t.Fatalf("a refused provision must persist no billable row, got %+v", pools)
	}
}

// The funded path still works, is billed the REAL rate for every node, and lands
// exactly one billable row carrying that rate — the row the hourly sweep reads.
func TestCreateNodePoolMeteredFundedProvisionsBillsOnceAndPersists(t *testing.T) {
	installBaseStore(t)
	c := commerceOf(t, 100000000)

	cloud := &fakeCloudPool{}
	pool, err := createNodePoolMetered(cloud, rateH100, "acme", "research", "do", "cl-1",
		&service.CreateNodePoolSpec{Name: "gpu", Size: "gpu-h100x8-640gb", Count: 4})
	if err != nil {
		t.Fatalf("a funded org must be able to provision: %v", err)
	}
	if cloud.created == nil || cloud.created.Count != 4 {
		t.Fatalf("upstream got %+v, want a 4-node pool", cloud.created)
	}
	_, debits, amount := c.state()
	if debits != 1 || amount != 3178*4 {
		t.Fatalf("the first hour must be billed exactly once at rate x nodes, got %d debits of %d cents", debits, amount)
	}
	stored, err := GetNodePool("acme/gpu")
	if err != nil || stored == nil {
		t.Fatalf("the provisioned pool must be a billable row: %v", err)
	}
	if stored.CostPerHour != 3178 || stored.Count != 4 || stored.State != "Active" ||
		stored.OrgID != "acme" || stored.ProjectID != "research" || stored.PoolID != pool.PoolID {
		t.Fatalf("the row must describe what was provisioned, got %+v", stored)
	}
}

// The count the UPSTREAM is asked for is the count the org was authorized and
// billed for. A non-positive count used to reach DigitalOcean as-is while the
// gate priced a floor of one, so the two numbers could differ — and the row the
// hourly sweep then bills would describe a pool that was never provisioned.
func TestCreateNodePoolMeteredProvisionsExactlyWhatItBills(t *testing.T) {
	for name, tc := range map[string]struct{ ask, want int }{
		"four nodes":         {4, 4},
		"zero floors to one": {0, 1},
		"negative floors":    {-5, 1},
	} {
		t.Run(name, func(t *testing.T) {
			installBaseStore(t)
			c := commerceOf(t, 100000000)

			cloud := &fakeCloudPool{}
			_, err := createNodePoolMetered(cloud, rateH100, "acme", "", "do", "cl-1",
				&service.CreateNodePoolSpec{Name: "gpu", Size: "gpu-h100x8-640gb", Count: tc.ask})
			if err != nil {
				t.Fatalf("a funded org must be able to provision: %v", err)
			}
			if cloud.created == nil || cloud.created.Count != tc.want {
				t.Fatalf("upstream was asked for %+v, want a %d-node pool", cloud.created, tc.want)
			}
			if _, debits, amount := c.state(); debits != 1 || amount != int64(rateH100*tc.want) {
				t.Fatalf("billed %d cents over %d debits, want %d for %d nodes",
					amount, debits, rateH100*tc.want, tc.want)
			}
			stored, err := GetNodePool("acme/gpu")
			if err != nil || stored == nil {
				t.Fatalf("read back: %v", err)
			}
			if stored.Count != tc.want {
				t.Fatalf("the billable row says %d nodes but %d were provisioned", stored.Count, tc.want)
			}
		})
	}
}

// An autoscaling pool is authorized at its CEILING: an org that can afford one
// node cannot open a pool allowed to reach sixteen.
func TestCreateNodePoolMeteredAutoscaleIsGatedAtTheCeiling(t *testing.T) {
	installBaseStore(t)
	commerceOf(t, 3178*4) // four node-hours

	cloud := &fakeCloudPool{}
	_, err := createNodePoolMetered(cloud, rateH100, "acme", "", "do", "cl-1",
		&service.CreateNodePoolSpec{Name: "gpu", Size: "gpu-h100x8-640gb",
			Count: 1, MinNodes: 1, MaxNodes: 16, AutoScale: true})

	if err == nil {
		t.Fatal("a pool allowed to reach 16 nodes must be authorized for 16, not for its starting 1")
	}
	if cloud.created != nil {
		t.Fatalf("REFUSED BUT PROVISIONED: upstream received %+v", cloud.created)
	}
}

// A scale UP is a provision and goes through the gate; a refused scale never
// reaches the upstream, so the pool keeps running at the size it was.
func TestScaleNodePoolMeteredRefusedReachesNoUpstream(t *testing.T) {
	installBaseStore(t)
	c := commerceOf(t, 0) // broke

	current := &service.NodePool{ID: "p-1", Name: "gpu", Size: "gpu-h100x8-640gb", Count: 2}
	cloud := &fakeCloudPool{}
	_, err := scaleNodePoolMetered(cloud, current, rateH100, "acme", "", "p-1", 8)

	if err == nil {
		t.Fatal("an unfunded org must not be able to grow a pool")
	}
	if cloud.updated != nil {
		t.Fatalf("REFUSED BUT SCALED: upstream received %+v", cloud.updated)
	}
	if reads, debits, _ := c.state(); reads == 0 || debits != 0 {
		t.Fatalf("the gate must read the balance (%d reads) and bill nothing (%d debits)", reads, debits)
	}
}

// A scale DOWN is never gated: refusing to shrink a pool because a balance is
// low would keep the meter running on nodes the customer asked to release. It
// holds even when the pool's slug has left the catalog and has no price at all.
func TestScaleNodePoolMeteredDownIsNeverBlocked(t *testing.T) {
	installBaseStore(t)
	commerceOf(t, 0) // broke

	if _, err := AddNodePool(&NodePool{Owner: "acme", Name: "gpu", OrgID: "acme", PoolID: "p-1",
		Size: "a-slug-the-upstream-delisted", Count: 8, State: "Active", CostPerHour: 3178,
		CreatedTime: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("AddNodePool: %v", err)
	}
	current := &service.NodePool{ID: "p-1", Name: "gpu", Size: "a-slug-the-upstream-delisted", Count: 8}
	cloud := &fakeCloudPool{}

	pool, err := scaleNodePoolMetered(cloud, current, 0, "acme", "", "p-1", 2)
	if err != nil {
		t.Fatalf("a scale DOWN must never be blocked: %v", err)
	}
	if cloud.updated == nil || cloud.updated.Count != 2 {
		t.Fatalf("the upstream must be asked to shrink, got %+v", cloud.updated)
	}
	if pool.Count != 2 {
		t.Fatalf("the row must follow the pool down, got count=%d", pool.Count)
	}
	if pool.CostPerHour != 3178 {
		t.Fatalf("an unpriceable slug must keep its stored rate, got %d", pool.CostPerHour)
	}
}
