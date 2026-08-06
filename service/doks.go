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
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

type NodeInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	DropletID string `json:"dropletId"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type NodePool struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	MinNodes  int               `json:"minNodes"`
	MaxNodes  int               `json:"maxNodes"`
	AutoScale bool              `json:"autoScale"`
	Nodes     []NodeInfo        `json:"nodes"`
	Tags      []string          `json:"tags"`
	Labels    map[string]string `json:"labels"`
}

type CreateNodePoolSpec struct {
	Name      string            `json:"name"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	MinNodes  int               `json:"minNodes"`
	MaxNodes  int               `json:"maxNodes"`
	AutoScale bool              `json:"autoScale"`
	Tags      []string          `json:"tags,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type DOKSClient struct {
	Client    *godo.Client
	ClusterID string
}

func NewDOKSClient(token, clusterID string) (*DOKSClient, error) {
	if token == "" {
		return nil, fmt.Errorf("DigitalOcean API token is required")
	}
	if clusterID == "" {
		return nil, fmt.Errorf("DOKS cluster ID is required")
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)
	client := godo.NewClient(oauthClient)

	return &DOKSClient{Client: client, ClusterID: clusterID}, nil
}

func nodePoolFromGodo(pool *godo.KubernetesNodePool) *NodePool {
	np := &NodePool{
		ID:        pool.ID,
		Name:      pool.Name,
		Size:      pool.Size,
		Count:     pool.Count,
		MinNodes:  pool.MinNodes,
		MaxNodes:  pool.MaxNodes,
		AutoScale: pool.AutoScale,
		Tags:      pool.Tags,
		Labels:    pool.Labels,
	}

	for _, node := range pool.Nodes {
		ni := NodeInfo{
			ID:        node.ID,
			Name:      node.Name,
			DropletID: node.DropletID,
		}
		if node.Status != nil {
			ni.Status = node.Status.State
		}
		if !node.CreatedAt.IsZero() {
			ni.CreatedAt = node.CreatedAt.Format("2006-01-02T15:04:05Z")
		}
		if !node.UpdatedAt.IsZero() {
			ni.UpdatedAt = node.UpdatedAt.Format("2006-01-02T15:04:05Z")
		}
		np.Nodes = append(np.Nodes, ni)
	}

	return np
}

func (c *DOKSClient) ListNodePools() ([]*NodePool, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	var allPools []*NodePool

	for {
		pools, resp, err := c.Client.Kubernetes.ListNodePools(context.TODO(), c.ClusterID, opt)
		if err != nil {
			return nil, fmt.Errorf("failed to list node pools: %w", err)
		}

		for _, pool := range pools {
			allPools = append(allPools, nodePoolFromGodo(pool))
		}

		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}

	return allPools, nil
}

func (c *DOKSClient) GetNodePool(poolID string) (*NodePool, error) {
	pool, _, err := c.Client.Kubernetes.GetNodePool(context.TODO(), c.ClusterID, poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node pool %s: %w", poolID, err)
	}

	return nodePoolFromGodo(pool), nil
}

func (c *DOKSClient) CreateNodePool(spec *CreateNodePoolSpec) (*NodePool, error) {
	req := &godo.KubernetesNodePoolCreateRequest{
		Name:      spec.Name,
		Size:      spec.Size,
		Count:     spec.Count,
		MinNodes:  spec.MinNodes,
		MaxNodes:  spec.MaxNodes,
		AutoScale: spec.AutoScale,
		Tags:      spec.Tags,
		Labels:    spec.Labels,
	}

	pool, _, err := c.Client.Kubernetes.CreateNodePool(context.TODO(), c.ClusterID, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create node pool: %w", err)
	}

	return nodePoolFromGodo(pool), nil
}

func (c *DOKSClient) UpdateNodePool(poolID string, spec *CreateNodePoolSpec) (*NodePool, error) {
	req := &godo.KubernetesNodePoolUpdateRequest{
		Name:      spec.Name,
		Count:     &spec.Count,
		MinNodes:  &spec.MinNodes,
		MaxNodes:  &spec.MaxNodes,
		AutoScale: &spec.AutoScale,
		Tags:      spec.Tags,
		Labels:    spec.Labels,
	}

	pool, _, err := c.Client.Kubernetes.UpdateNodePool(context.TODO(), c.ClusterID, poolID, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update node pool %s: %w", poolID, err)
	}

	return nodePoolFromGodo(pool), nil
}

func (c *DOKSClient) DeleteNodePool(poolID string) error {
	_, err := c.Client.Kubernetes.DeleteNodePool(context.TODO(), c.ClusterID, poolID)
	if err != nil {
		return fmt.Errorf("failed to delete node pool %s: %w", poolID, err)
	}

	return nil
}

func (c *DOKSClient) RecycleNodePoolNodes(poolID string, nodeIDs []string) error {
	req := &godo.KubernetesNodePoolRecycleNodesRequest{
		Nodes: nodeIDs,
	}

	_, err := c.Client.Kubernetes.RecycleNodePoolNodes(context.TODO(), c.ClusterID, poolID, req)
	if err != nil {
		return fmt.Errorf("failed to recycle nodes in pool %s: %w", poolID, err)
	}

	return nil
}

// IsNotFound reports whether err wraps a DigitalOcean 404 response, meaning the
// resource is already gone. Callers treat this as success when deleting.
func IsNotFound(err error) bool {
	var doErr *godo.ErrorResponse
	if errors.As(err, &doErr) && doErr.Response != nil {
		return doErr.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// KubernetesCluster is a DOKS cluster in the shape visor surfaces: identity,
// region, status and tags. Tags carry ownership (hanzo-org:<org>) used to scope a
// house-account cluster to the org that owns it.
type KubernetesCluster struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	RegionSlug string   `json:"regionSlug"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
}

func clusterFromGodo(c *godo.KubernetesCluster) *KubernetesCluster {
	kc := &KubernetesCluster{
		ID:         c.ID,
		Name:       c.Name,
		RegionSlug: c.RegionSlug,
		Tags:       c.Tags,
	}
	if c.Status != nil {
		kc.Status = string(c.Status.State)
	}
	return kc
}

// poolsFromGodo maps DO node pools to visor NodePools via the ONE pool mapper, so
// pools sourced from a cluster Get expand identically to those from ListNodePools.
func poolsFromGodo(gpools []*godo.KubernetesNodePool) []*NodePool {
	pools := make([]*NodePool, 0, len(gpools))
	for _, gp := range gpools {
		pools = append(pools, nodePoolFromGodo(gp))
	}
	return pools
}

// listClustersFull pages Kubernetes.List and re-Gets each cluster so RegionSlug,
// Status and the node pools (with their nodes) reflect authoritative per-cluster
// detail — List alone can return a lighter cluster. It is the ONE cluster
// enumeration, shared by ListClusters and the house-account node lister.
func listClustersFull(ctx context.Context, client *godo.Client) ([]*godo.KubernetesCluster, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	var out []*godo.KubernetesCluster
	for {
		clusters, resp, err := client.Kubernetes.List(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("failed to list kubernetes clusters: %w", err)
		}
		for _, cl := range clusters {
			full, _, err := client.Kubernetes.Get(ctx, cl.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to get kubernetes cluster %s: %w", cl.ID, err)
			}
			out = append(out, full)
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}
	return out, nil
}

// ListClusters returns every DOKS cluster visible to this client's token, in the
// clean KubernetesCluster shape. It is clustersByTag with no tag filter — the ONE
// cluster enumeration, so the tenant-scoped and account-wide lists never drift.
func (c *DOKSClient) ListClusters(ctx context.Context) ([]*KubernetesCluster, error) {
	return clustersByTag(ctx, c.Client, "")
}

// clustersByTag returns every cluster reachable by client whose tags contain
// wantTag, in the clean KubernetesCluster shape. An empty wantTag matches every
// cluster. It shares the ONE authoritative enumeration (listClustersFull) with the
// node lister, so a cluster's identity/region/status is sourced identically whether
// it surfaces as a cluster row or as its worker nodes — the exact tag-scoping
// kubernetesNodeMachinesByTag uses, hoisted to the cluster shape.
func clustersByTag(ctx context.Context, client *godo.Client, wantTag string) ([]*KubernetesCluster, error) {
	full, err := listClustersFull(ctx, client)
	if err != nil {
		return nil, err
	}
	out := make([]*KubernetesCluster, 0, len(full))
	for _, gc := range full {
		if wantTag != "" && !clusterHasTag(gc.Tags, wantTag) {
			continue
		}
		out = append(out, clusterFromGodo(gc))
	}
	return out, nil
}

// nodePoolMachines expands a cluster's node pools into one Machine per worker
// node — the SAME shape ListOrgMachines emits for a standalone droplet — so DOKS
// nodes merge into the fleet exactly like droplets. Id is the node's DropletID
// (the dedup key against the droplet list); Size is the pool's slug; Region and
// the owning cluster come from the cluster; State is the node's DO status. IPs are
// empty — the managed-Kubernetes API does not expose per-node addresses here — and
// are honestly omitted, not invented. A node with no DropletID yet (still
// provisioning) is skipped: it has no droplet, so no fleet identity.
func nodePoolMachines(cluster *KubernetesCluster, pools []*NodePool) []*Machine {
	var machines []*Machine
	for _, pool := range pools {
		for _, node := range pool.Nodes {
			if node.DropletID == "" {
				continue
			}
			machines = append(machines, &Machine{
				Id:       node.DropletID,
				Name:     node.Name,
				Provider: "DigitalOcean",
				Size:     pool.Size,
				Region:   cluster.RegionSlug,
				State:    node.Status,
				Tag:      "doks-cluster:" + cluster.Name,
			})
		}
	}
	return machines
}

// clusterHasTag reports whether a cluster carries an exact tag — the membership
// check that scopes a house-account cluster to its owning org.
func clusterHasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// kubernetesNodeMachinesByTag returns one Machine per DOKS worker node for every
// cluster reachable by client whose tags contain wantTag. An empty wantTag matches
// every cluster. Nodes are expanded from each cluster's own pools (returned by the
// authoritative Get in listClustersFull), so no per-cluster pool re-list is needed.
func kubernetesNodeMachinesByTag(ctx context.Context, client *godo.Client, wantTag string) ([]*Machine, error) {
	clusters, err := listClustersFull(ctx, client)
	if err != nil {
		return nil, err
	}
	var machines []*Machine
	for _, gc := range clusters {
		if wantTag != "" && !clusterHasTag(gc.Tags, wantTag) {
			continue
		}
		machines = append(machines, nodePoolMachines(clusterFromGodo(gc), poolsFromGodo(gc.NodePools))...)
	}
	return machines, nil
}

// NodeMachines returns one Machine per worker node in THIS client's cluster — the
// BYOC path where a Provider names a single cluster. The cluster Get supplies the
// region/name; ListNodePools supplies the pools whose nodes become the machines.
func (c *DOKSClient) NodeMachines(ctx context.Context) ([]*Machine, error) {
	gc, _, err := c.Client.Kubernetes.Get(ctx, c.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes cluster %s: %w", c.ClusterID, err)
	}
	pools, err := c.ListNodePools()
	if err != nil {
		return nil, err
	}
	return nodePoolMachines(clusterFromGodo(gc), pools), nil
}

// ---- cluster lifecycle (create / detail / delete) ----

// CreateClusterSpec is the minimal request to provision a DOKS cluster: identity
// (name), placement (region), the Kubernetes version, and ONE initial node pool.
// It is deliberately small — the resell surface provisions a single-pool cluster;
// further pools are added through the node-pool surface. Additional pools can be
// grown later; this is the create seed, not the full topology.
type CreateClusterSpec struct {
	Name     string                `json:"name"`
	Region   string                `json:"region"`
	Version  string                `json:"version"`
	NodePool CreateClusterNodePool `json:"nodePool"`
}

// CreateClusterNodePool is the initial worker pool of a new cluster: its instance
// size and how many nodes. Name is optional (defaults to "<cluster>-pool").
type CreateClusterNodePool struct {
	Name  string `json:"name,omitempty"`
	Size  string `json:"size"`
	Count int    `json:"count"`
}

// KubernetesClusterDetail is a cluster plus its node pools and the worker nodes
// expanded as fleet Machines — the ONE detail shape for GET .../clusters/:id. It
// embeds KubernetesCluster so identity/region/status/tags flatten into the same
// top-level JSON the list emits; NodePools carries the authoritative pool topology
// and Nodes the per-worker machines (the SAME shape ListOrgMachines emits).
type KubernetesClusterDetail struct {
	KubernetesCluster
	NodePools []*NodePool `json:"nodePools"`
	Nodes     []*Machine  `json:"nodes"`
}

// clusterDetailFromGodo assembles the detail from an authoritative cluster Get
// (which carries the node pools with their nodes). Pools and node machines are
// mapped through the ONE pool/node mappers, so a cluster's nodes are identical
// whether they surface here or through the fleet node lister.
func clusterDetailFromGodo(gc *godo.KubernetesCluster) *KubernetesClusterDetail {
	cluster := clusterFromGodo(gc)
	pools := poolsFromGodo(gc.NodePools)
	return &KubernetesClusterDetail{
		KubernetesCluster: *cluster,
		NodePools:         pools,
		Nodes:             nodePoolMachines(cluster, pools),
	}
}

// GetCluster returns one cluster's full detail — identity, node pools and the
// worker nodes as Machines. The Get is authoritative: it carries the pools (with
// their nodes) directly, so no separate pool re-list is needed.
func (c *DOKSClient) GetCluster(ctx context.Context, id string) (*KubernetesClusterDetail, error) {
	gc, _, err := c.Client.Kubernetes.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes cluster %s: %w", id, err)
	}
	return clusterDetailFromGodo(gc), nil
}

// buildClusterCreateRequest maps a CreateClusterSpec + ownership tags to the godo
// request. PURE (no I/O), so the spec→request contract is unit-tested without a
// client. Ergonomic defaults: an empty version resolves to DO's "latest" alias
// (newest patch of the default minor), an unnamed pool becomes "<cluster>-pool",
// and a non-positive count floors at 1 (a cluster must have at least one worker).
// tags carry ownership (managed-by + hanzo-org:<org>) so the cluster associates to
// its org exactly like a droplet.
func buildClusterCreateRequest(spec *CreateClusterSpec, tags []string) *godo.KubernetesClusterCreateRequest {
	version := strings.TrimSpace(spec.Version)
	if version == "" {
		version = "latest"
	}
	return &godo.KubernetesClusterCreateRequest{
		Name:        spec.Name,
		RegionSlug:  spec.Region,
		VersionSlug: version,
		Tags:        tags,
		NodePools: []*godo.KubernetesNodePoolCreateRequest{{
			Name:  seedPoolName(spec),
			Size:  spec.NodePool.Size,
			Count: seedPoolCount(spec),
		}},
	}
}

// seedPoolName and seedPoolCount are the ONE answer to "what pool does this
// cluster get". Three places need them to agree and none may restate them: the
// upstream create request, the amount the org is authorized and debited for, and
// the billable row the hourly sweep reads. When the count floor lived separately
// in the request builder and in the money gate, "the quantity authorized is the
// quantity provisioned" was a coincidence of two matching literals; now it is one
// expression.
func seedPoolName(spec *CreateClusterSpec) string {
	if name := strings.TrimSpace(spec.NodePool.Name); name != "" {
		return name
	}
	return spec.Name + "-pool"
}

func seedPoolCount(spec *CreateClusterSpec) int {
	if spec.NodePool.Count < 1 {
		return 1 // a cluster must have at least one worker
	}
	return spec.NodePool.Count
}

// CreateCluster provisions a DOKS cluster from spec, tagging it with tags so it
// associates to the owning org (the node/cluster listers scope by that tag).
func (c *DOKSClient) CreateCluster(ctx context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error) {
	gc, _, err := c.Client.Kubernetes.Create(ctx, buildClusterCreateRequest(spec, tags))
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes cluster: %w", err)
	}
	return clusterFromGodo(gc), nil
}

// DeleteCluster destroys a cluster by id. A DO 404 (already gone) is the caller's
// success to interpret via IsNotFound — DeleteCluster itself reports the raw error.
func (c *DOKSClient) DeleteCluster(ctx context.Context, id string) error {
	if _, err := c.Client.Kubernetes.Delete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete kubernetes cluster %s: %w", id, err)
	}
	return nil
}
