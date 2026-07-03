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
