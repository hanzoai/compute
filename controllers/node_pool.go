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

// node_pool.go serves a node pool as a RESOURCE. The collection is the caller
// org's pools and the item is one of them; the METHOD says what to do with it:
//
//	GET    /v1/k8s/pools       list the caller org's pools
//	POST   /v1/k8s/pools       provision one
//	GET    /v1/k8s/pools/:id   read one
//	PUT    /v1/k8s/pools/:id   state what it should be
//	DELETE /v1/k8s/pools/:id   destroy it
//
// It lives under /v1/k8s beside clusters and nodes because a pool is a
// Kubernetes thing, and it is FLAT rather than nested under its cluster because
// the store keys a pool by (Owner, Name) and refuses two clusters the same name
// in one org (object.RecordSeedPool) — so a cluster segment would identify
// nothing the name does not, while the list every caller wants is the org's, not
// one cluster's.
//
// Five TYPED ops, so this noun is in the registry every projection reads —
// OpenAPI, MCP, the CLI, the by-name call plane — rather than only on the wire.
//
// COUNT IS A FIELD, NOT AN ENDPOINT. There was a sixth address, scale-node-pool,
// and what it changed was `count`, which the pool already publishes. It is a PUT
// on the pool now: a caller states the pool it wants and each half goes to the
// writer that owns it — the count to the provider through the metered scale, the
// autoscale bounds to the row. One address, one method, no verb wearing a noun's
// clothes.
//
// Every handler takes its tenant from ONE place: principal, the same rule the
// machine and cluster surfaces run. It used to take it from two. The
// authorization filter derives the object's owner from `?id=` or the request
// BODY, while these handlers read `?owner=` — so a create naming one org in the
// query and another in the body cleared authorization against the second and
// provisioned against the first's cloud credentials, balance and invoice. A
// tenant read from a different field than the one authorization judged is not a
// second opinion, it is a configured cloud account.
package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

// PoolRef addresses ONE of the caller org's node pools.
type PoolRef struct {
	// Id is the pool's name within the caller's org, from the URL path.
	Id string `json:"id"`
	caller
}

// Pools is every node pool the caller org owns.
type Pools struct {
	// Pools is one row per pool, each carrying its own cluster, size and count.
	Pools []*object.NodePool `json:"pools"`
}

// PoolSpec is a node pool to provision, and it is the whole request: the cloud
// to build it on rides in the body with the rest of the pool rather than in the
// query, because they describe one thing.
type PoolSpec struct {
	// Provider is the caller org's cloud to build on (e.g. "digitalocean"). It
	// names a Provider row of the caller's own org, which is where the credential
	// that provisions and the account that is billed both come from.
	Provider string `json:"provider" validate:"required"`
	// ClusterID is the cluster to add the pool to. Empty uses the provider's
	// default cluster.
	ClusterID string `json:"clusterId"`
	// Name is the pool's name, and its identity — the id every later read, write
	// and delete addresses it by.
	Name string `json:"name"`
	// Size is the provider size slug for each node (e.g. "gpu-h100x8-640gb"). A
	// size with no resale price provisions nothing.
	Size string `json:"size"`
	// Count is how many nodes the pool starts with; below one it starts with one.
	Count int `json:"count"`
	// MinNodes and MaxNodes bound the provider's autoscaler. MaxNodes is what the
	// org is authorized to spend up to, since it is what the pool may grow to.
	MinNodes int `json:"minNodes"`
	MaxNodes int `json:"maxNodes"`
	// AutoScale turns the provider's cluster autoscaler on for this pool.
	AutoScale bool `json:"autoScale"`
	// Tags and Labels are passed through to the provider.
	Tags   []string          `json:"tags,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	caller
}

// PoolState is what a node pool should be. It carries only what a client may
// decide: Size, State and CostPerHour are the PROVIDER's answers and are written
// by the paths that ask it.
type PoolState struct {
	// Id is the pool to write, from the URL path.
	Id string `json:"id"`
	// Count is the node count the pool should have — an absolute target, never a
	// delta. It is a POINTER because absent must mean UNCHANGED: reaching it
	// spends money at the provider, and a plain int would make "I only meant to
	// edit the bounds" read as "scale to nothing".
	Count *int `json:"count"`
	// MinNodes, MaxNodes and AutoScale are the autoscale bounds, and they are the
	// row's own: writing them touches no cloud.
	MinNodes  int  `json:"minNodes"`
	MaxNodes  int  `json:"maxNodes"`
	AutoScale bool `json:"autoScale"`
	caller
}

// pool resolves a request into the fully-qualified `owner/name` id of the node
// pool it addresses: the OWNER is always the caller's own org and the NAME is
// the `:id` path segment. It is the node-pool twin of machine
// (agent_binding.go) — the id a caller can build is always scoped to its own
// org, so no crafted path reaches another tenant's pool.
//
// A name carrying a slash is REFUSED rather than joined. An `owner/name` id was
// the old query-parameter form and it survives as %2F in a path segment; joined,
// it makes a three-token id, and the store splits an id on exactly two tokens and
// PANICS otherwise — a 500 with a stack where a 400 belongs.
func pool(c caller, id string) (string, error) {
	name := strings.TrimSpace(id)
	if name == "" {
		return "", zip.ErrBadRequest("pool id is required")
	}
	if strings.Contains(name, "/") {
		return "", zip.ErrBadRequest("pool id is a name, and a name has no slash in it")
	}
	_, org := principal(c.Authorization, c.Owner)
	if org == "" {
		return "", zip.ErrForbidden("unauthorized: no org context")
	}
	return util.GetIdFromOwnerAndName(org, name), nil
}

// ListPools returns every node pool the caller org owns, reconciled against the
// cloud first so the answer is what the provider actually has.
//
// Response: {"pools": [{"owner": "acme", "name": "gpu", "clusterId": "cl-1", "poolId": "p-1", "size": "gpu-h100x8-640gb", "count": 2, "state": "Active"}]}
func ListPools(_ context.Context, in *Scope) (*Pools, error) {
	_, owner := principal(in.Authorization, in.Owner)
	if owner == "" {
		return nil, zip.ErrForbidden("unauthorized: no org context")
	}
	if _, err := object.SyncNodePoolsCloud(owner); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	pools, err := object.GetNodePools(owner)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if pools == nil {
		pools = []*object.NodePool{}
	}
	return &Pools{Pools: pools}, nil
}

// CreatePool provisions a node pool on one of the caller org's clusters, on that
// org's own cloud credential, and answers the pool it built. The org is
// authorized for the pool's full first hour — at the ceiling it may autoscale to
// — before anything is provisioned, so a size that cannot be priced and a
// balance that cannot cover it both build nothing.
//
// Example: {"provider": "digitalocean", "clusterId": "cl-1", "name": "gpu", "size": "gpu-h100x8-640gb", "count": 2}
// Response: {"owner": "acme", "name": "gpu", "clusterId": "cl-1", "poolId": "p-1", "size": "gpu-h100x8-640gb", "count": 2, "state": "Active"}
func CreatePool(_ context.Context, in *PoolSpec) (*object.NodePool, error) {
	_, owner := principal(in.Authorization, in.Owner)
	if owner == "" {
		return nil, zip.ErrForbidden("unauthorized: no org context")
	}
	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		return nil, zip.ErrBadRequest("provider is required")
	}
	spec := service.CreateNodePoolSpec{
		Name:      strings.TrimSpace(in.Name),
		Size:      strings.TrimSpace(in.Size),
		Count:     in.Count,
		MinNodes:  in.MinNodes,
		MaxNodes:  in.MaxNodes,
		AutoScale: in.AutoScale,
		Tags:      in.Tags,
		Labels:    in.Labels,
	}
	created, err := object.CreateNodePoolCloud(owner, provider, strings.TrimSpace(in.ClusterID), &spec)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return created, nil
}

// GetPool returns one of the caller org's node pools, or 404 when the org has no
// pool of that name.
//
// Absent is 404 and not a 200 carrying null: "there is no such pool" is a fact
// about the address, and a caller that has to inspect the fields of a success to
// discover a miss is one that will forget to.
func GetPool(_ context.Context, in *PoolRef) (*object.NodePool, error) {
	id, err := pool(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	stored, err := object.GetNodePool(id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if stored == nil {
		return nil, zip.ErrNotFound("no such node pool")
	}
	return stored, nil
}

// ReplacePool states what one of the caller org's node pools should be, and each
// half goes to the writer that owns it: the COUNT to the provider through the
// metered scale, the autoscale bounds to the row. Growing is a provision and is
// authorized for the added nodes' first hour before the provider is touched;
// shrinking is never gated, because refusing to release nodes over a low balance
// keeps the meter running on compute the customer asked to give back.
//
// The count is optional and absent means unchanged. Everything else is REPLACED,
// which is what PUT means: a request that omits the bounds clears them.
//
// Example: {"count": 4, "minNodes": 2, "maxNodes": 8, "autoScale": true}
// Response: {"owner": "acme", "name": "gpu", "poolId": "p-1", "count": 4, "minNodes": 2, "maxNodes": 8, "autoScale": true}
func ReplacePool(_ context.Context, in *PoolState) (*object.NodePool, error) {
	id, err := pool(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	stored, err := object.GetNodePool(id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if stored == nil {
		return nil, zip.ErrNotFound("no such node pool")
	}
	if in.Count != nil {
		if *in.Count < 0 {
			return nil, zip.ErrBadRequest("count must be non-negative")
		}
		if _, err := object.ScaleNodePoolCloud(stored, *in.Count); err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
		}
	}
	if _, err := object.UpdateNodePool(id, &object.NodePool{
		MinNodes:  in.MinNodes,
		MaxNodes:  in.MaxNodes,
		AutoScale: in.AutoScale,
	}); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// Read the pool back rather than answering from either write: the count came
	// from the provider and the bounds from the request, and the caller asked
	// what the pool IS.
	written, err := object.GetNodePool(id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if written == nil {
		return nil, zip.ErrNotFound("no such node pool")
	}
	return written, nil
}

// RemovePool destroys one of the caller org's node pools at its provider and,
// only once the provider confirms it is gone, drops the row that bills it.
// Answers 204.
//
// Idempotent: an org with no pool of that name is already in the asked-for state
// and answers 204 too.
func RemovePool(_ context.Context, in *PoolRef) (*struct{}, error) {
	id, err := pool(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	owner, name := splitOwnerName(id)
	if _, err := object.DeleteNodePoolCloud(&object.NodePool{Owner: owner, Name: name}); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}
