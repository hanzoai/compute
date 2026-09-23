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

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"

	"github.com/hanzoai/compute/service/commercetest"
)

// A batch launched with count=N is just N machines named "<name>-000",
// "<name>-001", … — the same "%s-%03d" scheme the deleted fleet used, now the
// ONE naming primitive for a batch.
func TestBatchMemberName(t *testing.T) {
	want := []string{"crawler-000", "crawler-001", "crawler-002"}
	for i, w := range want {
		if got := batchMemberName("crawler", i); got != w {
			t.Errorf("batchMemberName(crawler, %d) = %q, want %q", i, got, w)
		}
	}
}

// newLaunchCtx builds a ZAP request context the way the router hands one to a
// handler, so resolveComputeApp/Project can read the threaded tenant scope.
func newLaunchCtx() *zip.Ctx {
	return zip.New(zip.Config{}).TestCtx("POST", "/v1/machines")
}

// resolveComputeApp/Project resolve the OPTIONAL scope exactly one way: the
// gateway-threaded X-App-ID / X-Project-ID tenant context wins; a body value is a
// fallback for a direct API caller; absent stays empty (a launch that omits scope
// is never broken).
func TestResolveComputeScope(t *testing.T) {
	// Threaded context is authoritative over a body fallback.
	ctx := newLaunchCtx()
	ctx.Locals(object.TenantContextAppIDKey, "web")
	ctx.Locals(object.TenantContextProjectIDKey, "api")
	c := &ApiController{}
	c.Ctx = ctx
	if got := c.resolveComputeApp("bodyapp"); got != "web" {
		t.Fatalf("app: threaded X-App-ID must win, got %q want web", got)
	}
	if got := c.resolveComputeProject("bodyproj"); got != "api" {
		t.Fatalf("project: threaded X-Project-ID must win, got %q want api", got)
	}

	// No header -> the launch-body value is the fallback.
	c2 := &ApiController{}
	c2.Ctx = newLaunchCtx()
	if got := c2.resolveComputeProject("bodyproj"); got != "bodyproj" {
		t.Fatalf("project: body fallback, got %q want bodyproj", got)
	}
	if got := c2.resolveComputeApp("bodyapp"); got != "bodyapp" {
		t.Fatalf("app: body fallback, got %q want bodyapp", got)
	}

	// Neither header nor body -> empty (optional scope never gates a launch).
	c3 := &ApiController{}
	c3.Ctx = newLaunchCtx()
	if got := c3.resolveComputeProject(""); got != "" {
		t.Fatalf("project: absent must stay empty, got %q", got)
	}
	if got := c3.resolveComputeApp(""); got != "" {
		t.Fatalf("app: absent must stay empty, got %q", got)
	}
}

// unionMachines merges the two DOKS node sources (platform hanzo-org tag + BYOC
// Provider.ClusterID) into ONE deduped fleet list. A DOKS-only node surfaces; a
// node whose droplet is ALSO in the droplet list dedups by droplet id; a
// same-name collision dedups by name; the FIRST source wins so its row is kept.
func TestUnionMachinesDedup(t *testing.T) {
	// Source 1 (platform): the authoritative rows — a DOKS node and a droplet already
	// on the fleet.
	platform := []*service.Machine{
		{Id: "111", Name: "prod-default-aaa", Provider: "DigitalOcean", Tag: "doks-cluster:prod"},
		{Id: "999", Name: "web-1", Provider: "DigitalOcean"},
	}
	// Source 2 (BYOC): the SAME droplet 111 again (must dedup by id, platform wins),
	// a row that collides by NAME only (web-1, no id overlap → dedup by name), and a
	// genuinely new BYOC-only node 222.
	byoc := []*service.Machine{
		{Id: "111", Name: "prod-default-aaa", Provider: "DigitalOcean", Tag: "doks-cluster:prod"},
		{Name: "web-1", Provider: "DigitalOcean"},
		{Id: "222", Name: "byoc-default-zzz", Provider: "DigitalOcean", Tag: "doks-cluster:byoc"},
	}

	got := unionMachines(platform, byoc)
	if len(got) != 3 {
		t.Fatalf("union want 3 deduped machines (111, 999/web-1, 222), got %d: %+v", len(got), got)
	}
	seen := map[string]*service.Machine{}
	for _, m := range got {
		seen[m.Id] = m
	}
	if _, ok := seen["111"]; !ok {
		t.Errorf("DOKS node 111 missing from union")
	}
	if _, ok := seen["222"]; !ok {
		t.Errorf("BYOC-only node 222 missing from union")
	}
	if _, ok := seen["999"]; !ok {
		t.Errorf("droplet 999/web-1 missing; name-collision dedup must not drop the platform row")
	}

	// Empty sources are honest empties, never nil (JSON encodes []).
	if got := unionMachines(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("empty union must be non-nil empty slice, got %#v", got)
	}
}

// Nodes must put an ARRAY on the wire even when the org has none.
//
// It is the difference between "no nodes" and "this service does not serve this
// op", and a reader on the far side of a version skew has nothing else to tell
// them apart: a build that answers `{}` or `{"nodes":null}` decodes into a caller
// as an empty fleet and reports nothing wrong. cloud folds these nodes into the
// world fleet, so the wrong answer there is a silently smaller estate.
func TestNodesIsAlwaysAnArray(t *testing.T) {
	b, err := json.Marshal(&Nodes{Nodes: unionMachines(nil, nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"nodes":[]}`; got != want {
		t.Fatalf("empty Nodes = %s, want %s", got, want)
	}
}

// ---- the launch gate ----

// launchCommerce stands up a fake commerce and reports what the launch path
// asked it. Counting balance READS is what tells "the gate looked and refused"
// apart from "the gate is not there" — both end in an error, and only the reads
// distinguish them.
type launchCommerce struct {
	mu     sync.Mutex
	reads  int
	debits int
}

func launchCommerceOf(t *testing.T, availableCents int64) *launchCommerce {
	t.Helper()
	c := &launchCommerce{}
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/balance"):
			c.reads++
			_ = json.NewEncoder(w).Encode(map[string]any{"available": availableCents, "currency": "usd"})
		case strings.HasSuffix(r.URL.Path, "/usage"):
			c.debits++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"transactionId":"tx","type":"usage"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return c
}

func (c *launchCommerce) state() (reads, debits int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, c.debits
}

// A machine is not launched for an org that cannot pay for its first hour, and
// the refusal costs nothing. Remove the gate and no balance is ever read — the
// launch goes straight at the cloud.
func TestLaunchMeteredRefusesBeforeItProvisions(t *testing.T) {
	c := launchCommerceOf(t, 0) // broke

	machine, err := launchMetered(context.Background(), "acme", "",
		&service.CreateMachineSpec{Name: "box", InstanceType: "p5.48xlarge"},
		service.SizeBySlug("p5.48xlarge"))

	if err == nil || machine != nil {
		t.Fatalf("an unfunded org must be refused, got machine=%+v err=%v", machine, err)
	}
	reads, debits := c.state()
	if reads == 0 {
		t.Fatal("the launch must consult the balance BEFORE it provisions")
	}
	if debits != 0 {
		t.Fatalf("a refused launch must bill nothing, got %d debits", debits)
	}
	if !strings.Contains(err.Error(), "balance") {
		t.Fatalf("a refused launch must fail on the money, not on the cloud: %v", err)
	}
}

// A size with no resale price never reaches the balance, let alone the cloud:
// an unpriceable launch is refused before anything is asked of anyone.
func TestLaunchMeteredRefusesAnUnpriceableSize(t *testing.T) {
	c := launchCommerceOf(t, 100000000)

	_, err := launchMetered(context.Background(), "acme", "",
		&service.CreateMachineSpec{Name: "box", InstanceType: "p5en.48xlarge"},
		&service.SizeInfo{Slug: "p5en.48xlarge"})

	if err == nil {
		t.Fatal("a size with no price must be refused")
	}
	if reads, debits := c.state(); reads != 0 || debits != 0 {
		t.Fatalf("an unpriceable launch must ask commerce nothing, got %d reads and %d debits", reads, debits)
	}
}
