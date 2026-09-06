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
	"time"

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

// ---- DOKS clusters + worker nodes ----
//
// The node-pool CRUD above manages a cluster's pools; the surface below reads the
// cluster inventory itself and, crucially, expands each pool's per-node droplet
// list into machine-shaped records so the fleet can show individual DOKS worker
// NODES (not just standalone droplets). Every field is a REAL DigitalOcean value —
// a node with no address on the pool object honestly carries empty IPs; nothing is
// fabricated.

// ClusterInfo is a DOKS cluster's identity, placement and health — no secrets. It
// is the value the /v1/kubernetes-clusters surface returns and the enumeration the
// house node path filters by org tag.
type ClusterInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Region    string   `json:"region"`
	Version   string   `json:"version"`
	Status    string   `json:"status"`
	NodePools int      `json:"nodePools"`
	NodeCount int      `json:"nodeCount"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"createdAt"`
}

func clusterInfoFromGodo(c *godo.KubernetesCluster) *ClusterInfo {
	ci := &ClusterInfo{
		ID:        c.ID,
		Name:      c.Name,
		Region:    c.RegionSlug,
		Version:   c.VersionSlug,
		Tags:      c.Tags,
		NodePools: len(c.NodePools),
	}
	if c.Status != nil {
		ci.Status = string(c.Status.State)
	}
	if !c.CreatedAt.IsZero() {
		ci.CreatedAt = c.CreatedAt.Format(time.RFC3339)
	}
	for _, p := range c.NodePools {
		ci.NodeCount += p.Count
	}
	return ci
}

// ListClusters returns every DOKS cluster visible to this client's token.
func (c *DOKSClient) ListClusters() ([]*ClusterInfo, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	var out []*ClusterInfo
	for {
		clusters, resp, err := c.Client.Kubernetes.List(context.TODO(), opt)
		if err != nil {
			return nil, fmt.Errorf("failed to list DOKS clusters: %w", err)
		}
		for _, cl := range clusters {
			out = append(out, clusterInfoFromGodo(cl))
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}
	return out, nil
}

// GetCluster returns this client's cluster identity/placement/health.
func (c *DOKSClient) GetCluster() (*ClusterInfo, error) {
	cluster, _, err := c.Client.Kubernetes.Get(context.TODO(), c.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get DOKS cluster %s: %w", c.ClusterID, err)
	}
	return clusterInfoFromGodo(cluster), nil
}

// nodeMachinesFromCluster expands a cluster's node pools into machine-shaped worker
// records — one Machine per node. Id is the underlying DROPLET id (so a node dedupes
// against the plain droplet list by id), Name is the k8s node name, Provider is
// DigitalOcean, Size is the pool slug, Region is the cluster region, State is the
// node's own state and Tag marks its cluster (doks-cluster:<name>). IPs are left
// empty: a DOKS worker's addresses are not carried on the node-pool object, and an
// honest blank is correct — never a fabricated address.
func nodeMachinesFromCluster(cluster *godo.KubernetesCluster) []*Machine {
	if cluster == nil {
		return nil
	}
	var out []*Machine
	for _, pool := range cluster.NodePools {
		for _, node := range pool.Nodes {
			m := &Machine{
				Id:          node.DropletID,
				Name:        node.Name,
				DisplayName: node.Name,
				Provider:    "DigitalOcean",
				Category:    "Kubernetes",
				Size:        pool.Size,
				Region:      cluster.RegionSlug,
				Tag:         "doks-cluster:" + cluster.Name,
			}
			if node.Status != nil {
				m.State = node.Status.State
			}
			if !node.CreatedAt.IsZero() {
				m.CreatedTime = node.CreatedAt.Format(time.RFC3339)
			}
			out = append(out, m)
		}
	}
	return out
}

// clusterNodesByID fetches a cluster and expands its worker nodes. Kubernetes.Get
// carries the node pools WITH their per-node droplet ids + state and the cluster
// region/name — everything a node record needs in one call.
func (c *DOKSClient) clusterNodesByID(clusterID string) ([]*Machine, error) {
	cluster, _, err := c.Client.Kubernetes.Get(context.TODO(), clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get DOKS cluster %s: %w", clusterID, err)
	}
	return nodeMachinesFromCluster(cluster), nil
}

// ClusterNodes returns this client's cluster's worker nodes as machine records.
func (c *DOKSClient) ClusterNodes() ([]*Machine, error) {
	return c.clusterNodesByID(c.ClusterID)
}

// newHouseDOKSClient builds a DOKS client on Hanzo's house DO token with no fixed
// cluster — used to enumerate/expand every house cluster tagged to an org.
func newHouseDOKSClient() (*DOKSClient, error) {
	hc, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	return &DOKSClient{Client: hc.Client}, nil
}

// listOrgKubernetesNodes enumerates every cluster visible to client, keeps those
// tagged hanzo-org:<org> (the SAME tenancy tag ListOrgMachines scopes droplets by)
// and expands each into worker-node machine records.
func listOrgKubernetesNodes(client *DOKSClient, org string) ([]*Machine, error) {
	clusters, err := client.ListClusters()
	if err != nil {
		return nil, err
	}
	want := orgTag(org)
	var out []*Machine
	for _, ci := range clusters {
		if !hasTag(ci.Tags, want) {
			continue
		}
		nodes, err := client.clusterNodesByID(ci.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, nodes...)
	}
	return out, nil
}

// listOrgKubernetesClusters is the cluster analogue of listOrgKubernetesNodes: the
// house-account clusters tagged to org.
func listOrgKubernetesClusters(client *DOKSClient, org string) ([]*ClusterInfo, error) {
	clusters, err := client.ListClusters()
	if err != nil {
		return nil, err
	}
	want := orgTag(org)
	out := make([]*ClusterInfo, 0, len(clusters))
	for _, ci := range clusters {
		if hasTag(ci.Tags, want) {
			out = append(out, ci)
		}
	}
	return out, nil
}

// ListOrgKubernetesNodesHouse returns the worker nodes of every house-account DOKS
// cluster tagged hanzo-org:<org>. Empty (nil, nil) when compute is not configured —
// the BYOC provider path still contributes on its own.
func ListOrgKubernetesNodesHouse(org string) ([]*Machine, error) {
	if !ComputeConfigured() {
		return nil, nil
	}
	client, err := newHouseDOKSClient()
	if err != nil {
		return nil, err
	}
	return listOrgKubernetesNodes(client, org)
}

// ListOrgKubernetesClustersHouse returns the house-account DOKS clusters tagged
// hanzo-org:<org>. Empty (nil, nil) when compute is not configured.
func ListOrgKubernetesClustersHouse(org string) ([]*ClusterInfo, error) {
	if !ComputeConfigured() {
		return nil, nil
	}
	client, err := newHouseDOKSClient()
	if err != nil {
		return nil, err
	}
	return listOrgKubernetesClusters(client, org)
}
