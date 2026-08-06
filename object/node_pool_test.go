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
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

type fakeNodePoolDeleter struct {
	err    error
	called bool
}

func (f *fakeNodePoolDeleter) DeleteNodePool(poolID string) error {
	f.called = true
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
		err := confirmCloudPoolDeleted(client, "pool-123")
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
