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

package object

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/hanzoai/compute/service"
)

type fakeNodePoolDeleter struct {
	err     error
	called  bool
	cluster string
}

func (f *fakeNodePoolDeleter) DeleteNodePool(_ context.Context, clusterID, poolID string) error {
	f.called = true
	f.cluster = clusterID
	return f.err
}

// doErrorResponse builds an error identical in shape to what godo returns for a
// non-2xx DO API response, so IsNotFound is exercised against the real type.
func doErrorResponse(status int) error {
	return &godo.ErrorResponse{
		Response: &http.Response{StatusCode: status},
		Message:  fmt.Sprintf("digitalocean returned %d", status),
	}
}

// TestConfirmCloudPoolDeleted pins the delete-flow decision: a successful or
// already-gone (404) provider response confirms deletion, while a transient 422
// (still provisioning) or any other error is propagated so the caller retries
// and the DB row is preserved.
func TestConfirmCloudPoolDeleted(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"provider deletes ok", nil, false},
		{"provider 404 already gone", doErrorResponse(http.StatusNotFound), false},
		{"wrapped 404 already gone", fmt.Errorf("failed to delete node pool p: %w", doErrorResponse(http.StatusNotFound)), false},
		{"422 still provisioning retries", doErrorResponse(http.StatusUnprocessableEntity), true},
		{"wrapped 422 still provisioning retries", fmt.Errorf("failed to delete node pool p: %w", doErrorResponse(http.StatusUnprocessableEntity)), true},
		{"500 propagates", doErrorResponse(http.StatusInternalServerError), true},
		{"opaque error propagates", errors.New("connection refused"), true},
	}
	for _, tc := range cases {
		client := &fakeNodePoolDeleter{err: tc.err}
		err := confirmCloudPoolDeleted(client, "cl-1", "pool-123")
		if client.cluster != "cl-1" {
			t.Errorf("%s: pool delete reached cluster %q, want cl-1", tc.name, client.cluster)
		}
		if !client.called {
			t.Errorf("%s: expected DeleteNodePool to be invoked on the provider", tc.name)
		}
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: confirmCloudPoolDeleted err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

// billedPool is a running, priced GPU pool — exactly what the hourly sweep bills.
func billedPool() *NodePool {
	now := time.Now().Format(time.RFC3339)
	return &NodePool{
		Owner: "acme", Name: "gpu", OrgID: "acme", ProjectID: "research",
		ClusterID: "cl-1", PoolID: "p-1", Provider: "do",
		Size: "gpu-h100x8-640gb", Count: 4, State: "Active", CostPerHour: 3178,
		MinNodes: 1, MaxNodes: 4, AutoScale: false,
		CreatedTime: now, UpdatedTime: now,
	}
}

// A customer could switch off their own meter with one request. The update wrote
// EVERY column from the body, and the sweep trusts State, Count and CostPerHour —
// so `{"state":"Deleted"}` removed a running eight-GPU pool from billing forever
// while DigitalOcean kept charging Hanzo for it. So did `{"count":0}`, and
// `{"costPerHour":1}` re-priced it to a cent an hour.
func TestUpdateNodePoolCannotDisableTheMeter(t *testing.T) {
	for name, body := range map[string]*NodePool{
		"state Deleted":     {State: "Deleted"},
		"count zeroed":      {Count: 0},
		"price undercut":    {CostPerHour: 1},
		"size downgraded":   {Size: "s-1vcpu-1gb"},
		"provider unlinked": {ClusterID: "", PoolID: ""},
		"org reassigned":    {OrgID: "someone-else", ProjectID: "elsewhere"},
		"create hour reset": {CreatedTime: time.Now().Format(time.RFC3339)},
		"everything at once": {State: "Deleted", Count: 0, CostPerHour: 1, Size: "s-1vcpu-1gb",
			OrgID: "someone-else", ClusterID: "", PoolID: ""},
	} {
		t.Run(name, func(t *testing.T) {
			installBaseStore(t)
			before := billedPool()
			if _, err := AddNodePool(before); err != nil {
				t.Fatalf("AddNodePool: %v", err)
			}

			if _, err := UpdateNodePool("acme/gpu", body); err != nil {
				t.Fatalf("UpdateNodePool: %v", err)
			}

			after, err := GetNodePool("acme/gpu")
			if err != nil || after == nil {
				t.Fatalf("read back: %v", err)
			}
			if after.State != before.State || after.Count != before.Count ||
				after.CostPerHour != before.CostPerHour || after.Size != before.Size ||
				after.OrgID != before.OrgID || after.ProjectID != before.ProjectID ||
				after.ClusterID != before.ClusterID || after.PoolID != before.PoolID ||
				after.Provider != before.Provider || after.CreatedTime != before.CreatedTime {
				t.Fatalf("a request body reached the meter:\n before %+v\n after  %+v", before, after)
			}

			// The pool is still in the set the cross-org sweep bills.
			pools := []*NodePool{}
			if err := GetAllNodePools(&pools); err != nil {
				t.Fatalf("GetAllNodePools: %v", err)
			}
			if len(pools) != 1 || pools[0].CostPerHour != 3178 || pools[0].Count != 4 {
				t.Fatalf("the pool must still be billable at its real rate, got %+v", pools)
			}
		})
	}
}

// The autoscale bounds ARE the customer's to set — a whitelist that refuses
// everything is not a whitelist, it is an outage.
func TestUpdateNodePoolAppliesTheEditableFields(t *testing.T) {
	installBaseStore(t)
	if _, err := AddNodePool(billedPool()); err != nil {
		t.Fatalf("AddNodePool: %v", err)
	}

	ok, err := UpdateNodePool("acme/gpu", &NodePool{MinNodes: 2, MaxNodes: 16, AutoScale: true})
	if err != nil || !ok {
		t.Fatalf("UpdateNodePool = %v, %v; want true, nil", ok, err)
	}
	after, err := GetNodePool("acme/gpu")
	if err != nil || after == nil {
		t.Fatalf("read back: %v", err)
	}
	if after.MinNodes != 2 || after.MaxNodes != 16 || !after.AutoScale {
		t.Fatalf("the editable fields must be applied, got min=%d max=%d auto=%v",
			after.MinNodes, after.MaxNodes, after.AutoScale)
	}
	if after.UpdatedTime == "" {
		t.Fatal("an applied edit must stamp updated_time")
	}
}

// A body naming a different owner/name rewrote the primary key, moving the row
// out from under its own meter and corrupting whatever sat at the new key.
func TestUpdateNodePoolCannotRewriteThePrimaryKey(t *testing.T) {
	installBaseStore(t)
	if _, err := AddNodePool(billedPool()); err != nil {
		t.Fatalf("AddNodePool: %v", err)
	}

	if _, err := UpdateNodePool("acme/gpu", &NodePool{Owner: "acme", Name: "somewhere-else", MaxNodes: 9}); err != nil {
		t.Fatalf("UpdateNodePool: %v", err)
	}

	orig, err := GetNodePool("acme/gpu")
	if err != nil || orig == nil {
		t.Fatalf("the addressed pool must survive under its own key: %v", err)
	}
	if orig.MaxNodes != 9 {
		t.Fatalf("the edit must land on the addressed pool, got max=%d", orig.MaxNodes)
	}
	moved, err := GetNodePool("acme/somewhere-else")
	if err != nil {
		t.Fatalf("GetNodePool: %v", err)
	}
	if moved != nil {
		t.Fatalf("a body must not conjure a row at another key, got %+v", moved)
	}
}

// ---- the row is not a thing a customer can rename out from under the meter ----

// Two clusters, two seed pools, ONE name — which any tenant can do twice in a row,
// because the seed pool's name comes straight out of the cluster-create body. The
// rows collide on (Owner, Name), and the second create used to UPDATE the first
// one's row in place: the row then pointed at cluster two, and cluster one's rate,
// project and create hour were gone.
//
// A retry of the SAME cluster must still be idempotent — that is what keeps a
// retried create from re-opening an hour the first attempt already debited — so
// the two cases are told apart by the cluster, not by the key.
func TestRecordSeedPoolRefusesToRepointAnotherClustersRow(t *testing.T) {
	installBaseStore(t)

	first := service.SeedPool{Org: "acme", ClusterID: "cl-first", Name: "pool",
		Size: "gpu-h100x8-640gb", Count: 4, CentsHour: 3178, Project: "research"}
	if err := RecordSeedPool(first); err != nil {
		t.Fatalf("first cluster: %v", err)
	}

	second := first
	second.ClusterID = "cl-second"
	second.Project = "other"
	err := RecordSeedPool(second)
	if err == nil {
		t.Fatal("a second cluster must not take over the first one's billable row")
	}
	if !strings.Contains(err.Error(), "cl-first") || !strings.Contains(err.Error(), "cl-second") {
		t.Fatalf("the refusal must name both clusters so the collision is diagnosable: %v", err)
	}

	kept, err := GetNodePool("acme/pool")
	if err != nil || kept == nil {
		t.Fatalf("read back: %v", err)
	}
	if kept.ClusterID != "cl-first" || kept.ProjectID != "research" {
		t.Fatalf("the first cluster's row was overwritten: %+v", kept)
	}
}

// The SAME cluster re-recorded is a retry, and a retry keeps the original create
// hour so the exactly-once window is never re-opened.
func TestRecordSeedPoolStillRetriesTheSameCluster(t *testing.T) {
	installBaseStore(t)

	seed := service.SeedPool{Org: "acme", ClusterID: "cl-1", Name: "pool",
		Size: "gpu-h100x8-640gb", Count: 4, CentsHour: 3178}
	if err := RecordSeedPool(seed); err != nil {
		t.Fatalf("first record: %v", err)
	}
	before, _ := GetNodePool("acme/pool")
	if err := RecordSeedPool(seed); err != nil {
		t.Fatalf("a retry of the same cluster must not fail: %v", err)
	}
	after, _ := GetNodePool("acme/pool")
	if after.CreatedTime != before.CreatedTime {
		t.Fatalf("a retry must keep the original create hour: %q then %q", before.CreatedTime, after.CreatedTime)
	}
}

// ---- a running pool's row is not dropped on the customer's say-so ----

// The delete used to gate the whole upstream round-trip on the BODY carrying a
// PoolID and a Provider. Send `{"name":"gpu"}` and nothing else and compute never
// asked DigitalOcean anything: it deleted the row and returned success, while the
// pool kept running on the configured cloud account with nothing left to bill it.
//
// Linkage is read from the STORED row now, so the omission changes nothing.
func TestDeleteNodePoolCloudIgnoresABodyThatOmitsTheLinkage(t *testing.T) {
	installBaseStore(t)
	stored := billedPool()
	stored.Provider = "" // a platform cluster's seed pool names no per-org provider
	if _, err := AddNodePool(stored); err != nil {
		t.Fatalf("AddNodePool: %v", err)
	}

	// Exactly what the handler builds from `{"name":"gpu"}`: the owner is the
	// authenticated org, and NOTHING else is known.
	_, err := DeleteNodePoolCloud(&NodePool{Owner: "acme", Name: "gpu"})
	if err == nil {
		t.Fatal("with no provider token the provider cannot be reached, so the delete must fail rather than drop the row")
	}

	still, err := GetNodePool("acme/gpu")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if still == nil {
		t.Fatal("THE METER OF RECORD WAS DROPPED without the provider ever confirming the pool is gone")
	}
	if still.PoolID != "p-1" || still.ClusterID != "cl-1" || still.CostPerHour != 3178 {
		t.Fatalf("the surviving row must be intact: %+v", still)
	}
}

// A row naming a provider the store does not have is an ERROR, not permission to
// drop the row. Skipping the check there was the same leak by a different road.
func TestDeleteNodePoolCloudRefusesWhenTheProviderCannotBeResolved(t *testing.T) {
	installBaseStore(t)
	if _, err := AddNodePool(billedPool()); err != nil { // Provider "do", which does not exist
		t.Fatalf("AddNodePool: %v", err)
	}

	if _, err := DeleteNodePoolCloud(&NodePool{Owner: "acme", Name: "gpu"}); err == nil {
		t.Fatal("an unresolvable provider must refuse the delete")
	}
	if p, err := GetNodePool("acme/gpu"); err != nil || p == nil {
		t.Fatalf("the billable row must survive an unconfirmed delete (err=%v)", err)
	}
}

// A DB-only row — no upstream linkage at all — has no provider to confirm
// anything with, so it is removed directly. That is the one case where the row IS
// the whole truth.
func TestDeleteNodePoolCloudRemovesADbOnlyRow(t *testing.T) {
	installBaseStore(t)
	local := billedPool()
	local.PoolID, local.ClusterID, local.Provider = "", "", ""
	if _, err := AddNodePool(local); err != nil {
		t.Fatalf("AddNodePool: %v", err)
	}

	ok, err := DeleteNodePoolCloud(&NodePool{Owner: "acme", Name: "gpu"})
	if err != nil || !ok {
		t.Fatalf("a DB-only row must delete cleanly: ok=%v err=%v", ok, err)
	}
	if p, _ := GetNodePool("acme/gpu"); p != nil {
		t.Fatalf("the row should be gone, got %+v", p)
	}
}

// A name that is not this org's row deletes nothing, and says so.
func TestDeleteNodePoolCloudOnAnAbsentRowIsANoOp(t *testing.T) {
	installBaseStore(t)
	ok, err := DeleteNodePoolCloud(&NodePool{Owner: "acme", Name: "nosuch"})
	if err != nil {
		t.Fatalf("an absent row is not an error: %v", err)
	}
	if ok {
		t.Fatal("nothing was deleted, so the answer is false")
	}
}
