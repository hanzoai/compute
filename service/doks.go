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

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/digitalocean/godo"
	"google.golang.org/api/googleapi"
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

	return &DOKSClient{Client: newDOClient(token, nil), ClusterID: clusterID}, nil
}

// newDOKSCloudClient builds a client for CLUSTER-level work, where there is no
// cluster id yet. NewDOKSClient requires one because its node-pool methods are
// scoped to a cluster; the cluster list/create/delete are not.
func newDOKSCloudClient(token string) (*DOKSClient, error) {
	if token == "" {
		return nil, fmt.Errorf("DigitalOcean API token is required")
	}
	// Same construction the machine plane uses, so there is one place a DO client
	// is built and one place its transport can be changed.
	m, err := newMachineDigitalOceanClient(token, "", "", nil)
	if err != nil {
		return nil, err
	}
	return &DOKSClient{Client: m.Client}, nil
}

// Provider satisfies KubernetesClientInterface.
func (c *DOKSClient) Provider() string { return providerDigitalOcean }

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

func (c *DOKSClient) ListNodePools(ctx context.Context, clusterID string) ([]*NodePool, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	var allPools []*NodePool

	for {
		pools, resp, err := c.Client.Kubernetes.ListNodePools(ctx, clusterID, opt)
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

func (c *DOKSClient) GetNodePool(ctx context.Context, clusterID, poolID string) (*NodePool, error) {
	pool, _, err := c.Client.Kubernetes.GetNodePool(ctx, clusterID, poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node pool %s: %w", poolID, err)
	}

	return nodePoolFromGodo(pool), nil
}

func (c *DOKSClient) CreateNodePool(ctx context.Context, clusterID string, spec *CreateNodePoolSpec) (*NodePool, error) {
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

	pool, _, err := c.Client.Kubernetes.CreateNodePool(ctx, clusterID, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create node pool: %w", err)
	}

	return nodePoolFromGodo(pool), nil
}

// ScaleNodePool sets how many nodes a pool runs. Only the count is sent: every
// other field of DO's update request is a pointer, and a nil one leaves the
// pool's own value alone — so a scale cannot silently rewrite a pool's autoscale
// bounds or labels back to whatever the caller last read.
func (c *DOKSClient) ScaleNodePool(ctx context.Context, clusterID, poolID string, count int) (*NodePool, error) {
	pool, _, err := c.Client.Kubernetes.UpdateNodePool(ctx, clusterID, poolID,
		&godo.KubernetesNodePoolUpdateRequest{Count: &count})
	if err != nil {
		return nil, fmt.Errorf("failed to scale node pool %s: %w", poolID, err)
	}

	return nodePoolFromGodo(pool), nil
}

func (c *DOKSClient) DeleteNodePool(ctx context.Context, clusterID, poolID string) error {
	_, err := c.Client.Kubernetes.DeleteNodePool(ctx, clusterID, poolID)
	if err != nil {
		return fmt.Errorf("failed to delete node pool %s: %w", poolID, err)
	}

	return nil
}

func (c *DOKSClient) RecycleNodePoolNodes(ctx context.Context, clusterID, poolID string, nodeIDs []string) error {
	req := &godo.KubernetesNodePoolRecycleNodesRequest{
		Nodes: nodeIDs,
	}

	_, err := c.Client.Kubernetes.RecycleNodePoolNodes(ctx, clusterID, poolID, req)
	if err != nil {
		return fmt.Errorf("failed to recycle nodes in pool %s: %w", poolID, err)
	}

	return nil
}

// IsNotFound reports whether err says the resource is already gone, in the words
// of whichever cloud answered. Callers treat this as success when deleting and
// as "ask the next cloud" when locating.
func IsNotFound(err error) bool {
	var doErr *godo.ErrorResponse
	if errors.As(err, &doErr) && doErr.Response != nil {
		return doErr.Response.StatusCode == http.StatusNotFound
	}
	var eksErr *ekstypes.ResourceNotFoundException
	if errors.As(err, &eksErr) {
		return true
	}
	var gErr *googleapi.Error
	return errors.As(err, &gErr) && gErr.Code == http.StatusNotFound
}

// KubernetesCluster is a DOKS cluster in the shape visor surfaces: identity,
// region, status and tags. Tags carry ownership (hanzo-org:<org>) used to scope a
// platform-account cluster to the org that owns it.
type KubernetesCluster struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	RegionSlug string   `json:"regionSlug"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
	// Provider names the cloud this cluster is on. Additive and omitted when
	// unset, so a single-cloud response is byte-identical to what it was.
	Provider string `json:"provider,omitempty"`
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

// liveNodes is how many nodes a pool ACTUALLY has, which is the number that
// bills. Every one of them is a droplet Hanzo is paying the upstream for, so the
// count that charges is the count that exists — not the count anybody asked for.
//
// The node list is preferred over the declared Count because an autoscaling pool
// grows and shrinks without a request reaching visor: nothing writes the new
// number down anywhere visor controls, and the nodes are the only ground truth.
// Count is the fallback for a pool the provider returned without node detail, so
// a missing list is never read as "free".
func liveNodes(p *NodePool) int {
	if n := len(p.Nodes); n > 0 {
		return n
	}
	return p.Count
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
// enumeration, shared by ListClusters and the platform-account node lister.
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
// check that scopes a platform-account cluster to its owning org.
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
	pools, err := c.ListNodePools(ctx, c.ClusterID)
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
	// Provider picks the cloud. Optional while exactly one is configured;
	// required once there are several, because guessing puts a customer's
	// cluster on a cloud they did not choose.
	Provider string `json:"provider,omitempty"`
	// SubnetIDs places the cluster on a VPC, for the clouds that ask (EKS).
	// Empty means the account's default VPC, one subnet per zone.
	SubnetIDs []string `json:"subnetIds,omitempty"`
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

// GetCredentials asks DigitalOcean to mint a short-lived credential for one
// cluster's apiserver. The token DO returns expires (an hour is the longest it
// will sign for), which is exactly why the caller re-asks rather than storing it.
//
// The account token never travels: what comes back reaches this one cluster and
// nothing else on the account.
func (c *DOKSClient) GetCredentials(ctx context.Context, clusterID string) (*ClusterCredentials, error) {
	expiry := credentialSeconds
	creds, _, err := c.Client.Kubernetes.GetCredentials(ctx, clusterID,
		&godo.KubernetesClusterCredentialsGetRequest{ExpirySeconds: &expiry})
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials for kubernetes cluster %s: %w", clusterID, err)
	}
	return &ClusterCredentials{
		Endpoint: creds.Server,
		CAData:   creds.CertificateAuthorityData,
		Token:    creds.Token,
		Expiry:   creds.ExpiresAt,
	}, nil
}

// credentialSeconds is how long a minted apiserver credential lives. An hour is
// DigitalOcean's ceiling and long enough that a refresh is rare, short enough
// that a leaked one is worth little.
const credentialSeconds = 3600
