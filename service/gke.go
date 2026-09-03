// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/container/v1"
)

// providerGCP is Google Cloud's name as NewMachineClient spells it.
const providerGCP = "Google Cloud"

// GKEClient is Google Cloud's managed-Kubernetes face, bound to one project and
// one location (a zone or a region — GKE takes either). Clusters and pools are
// addressed by name within it; nodes are the compute instances GKE labels with
// the cluster's name.
type GKEClient struct {
	svc *container.Service
	// vm is the compute REST face, for the instances behind a pool.
	vm       MachineGcpClient
	project  string
	location string
	// ts mints the bearer; nil when the carrier attaches it, in which case no
	// token can be handed out.
	ts oauth2.TokenSource
}

func (c *GKEClient) Provider() string { return providerGCP }

func (c *GKEClient) parent() string { return "projects/" + c.project + "/locations/" + c.location }
func (c *GKEClient) cluster(id string) string { return c.parent() + "/clusters/" + id }
func (c *GKEClient) pool(id, pool string) string {
	return c.cluster(id) + "/nodePools/" + pool
}

// ---- pure mappers ----

func clusterFromGKE(cl *container.Cluster) *KubernetesCluster {
	return &KubernetesCluster{
		ID:         cl.Name,
		Name:       cl.Name,
		RegionSlug: cl.Location,
		Status:     cl.Status,
		Tags:       tagList(cl.ResourceLabels),
		Provider:   providerGCP,
	}
}

// poolFromGKE maps a node pool. GKE states the count a pool started at per zone
// and nothing live on the pool itself; the instances behind it are the ground
// truth and surface as the cluster's nodes.
func poolFromGKE(p *container.NodePool) *NodePool {
	np := &NodePool{ID: p.Name, Name: p.Name, Count: int(p.InitialNodeCount)}
	if p.Config != nil {
		np.Size = p.Config.MachineType
		np.Labels = p.Config.Labels
		np.Tags = tagList(p.Config.ResourceLabels)
	}
	if a := p.Autoscaling; a != nil && a.Enabled {
		np.AutoScale = true
		np.MinNodes = int(a.MinNodeCount)
		np.MaxNodes = int(a.MaxNodeCount)
	}
	return np
}

func buildGKECluster(spec *CreateClusterSpec, tags []string) *container.Cluster {
	cl := &container.Cluster{
		Name:           spec.Name,
		ResourceLabels: tagMap(tags),
		NodePools: []*container.NodePool{buildGKEPool(&CreateNodePoolSpec{
			Name: seedPoolName(spec), Size: spec.NodePool.Size, Count: seedPoolCount(spec),
		})},
	}
	if v := strings.TrimSpace(spec.Version); v != "" {
		cl.InitialClusterVersion = v
	}
	return cl
}

// buildGKEPool always sends the count: the field is omitted when zero, and an
// omitted count is three nodes on GKE, not none.
func buildGKEPool(spec *CreateNodePoolSpec) *container.NodePool {
	p := &container.NodePool{
		Name:             spec.Name,
		InitialNodeCount: int64(spec.Count),
		ForceSendFields:  []string{"InitialNodeCount"},
		Config: &container.NodeConfig{
			MachineType:    spec.Size,
			Labels:         spec.Labels,
			ResourceLabels: tagMap(spec.Tags),
		},
	}
	if spec.AutoScale {
		p.Autoscaling = &container.NodePoolAutoscaling{
			Enabled: true, MinNodeCount: int64(spec.MinNodes), MaxNodeCount: int64(spec.MaxNodes),
		}
	}
	return p
}

// ---- clusters ----

func (c *GKEClient) ListClusters(ctx context.Context) ([]*KubernetesCluster, error) {
	resp, err := c.svc.Projects.Locations.Clusters.List(c.parent()).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gke: list clusters: %w", err)
	}
	out := make([]*KubernetesCluster, 0, len(resp.Clusters))
	for _, cl := range resp.Clusters {
		out = append(out, clusterFromGKE(cl))
	}
	return out, nil
}

func (c *GKEClient) get(ctx context.Context, id string) (*container.Cluster, error) {
	cl, err := c.svc.Projects.Locations.Clusters.Get(c.cluster(id)).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gke: get cluster %s: %w", id, err)
	}
	return cl, nil
}

func (c *GKEClient) GetCluster(ctx context.Context, id string) (*KubernetesClusterDetail, error) {
	cl, err := c.get(ctx, id)
	if err != nil {
		return nil, err
	}
	pools := make([]*NodePool, 0, len(cl.NodePools))
	for _, p := range cl.NodePools {
		pools = append(pools, poolFromGKE(p))
	}
	nodes, err := c.nodes(ctx, id)
	if err != nil {
		return nil, err
	}
	return &KubernetesClusterDetail{KubernetesCluster: *clusterFromGKE(cl), NodePools: pools, Nodes: nodes}, nil
}

// computeAggregated is the compute API's cross-zone instance list, keyed by scope.
type computeAggregated struct {
	Items map[string]struct {
		Instances []computeInstance `json:"instances"`
	} `json:"items"`
}

// nodes are the instances GKE labels with the cluster's name, across every zone
// of the project — a regional cluster spreads them.
func (c *GKEClient) nodes(ctx context.Context, cluster string) ([]*Machine, error) {
	endpoint := fmt.Sprintf("%s/projects/%s/aggregated/instances?filter=%s", c.vm.computeURL(), c.project,
		url.QueryEscape("labels.goog-k8s-cluster-name="+cluster))
	var agg computeAggregated
	if err := c.vm.do(ctx, http.MethodGet, endpoint, &agg); err != nil {
		return nil, fmt.Errorf("gke: list nodes of %s: %w", cluster, err)
	}
	var out []*Machine
	for _, scope := range agg.Items {
		for i := range scope.Instances {
			m := getMachineFromComputeInstance(&scope.Instances[i])
			m.Provider = providerGCP
			m.Tag = "gke-cluster:" + cluster
			out = append(out, m)
		}
	}
	return out, nil
}

// NodeMachines is every worker on every cluster in this project and location.
func (c *GKEClient) NodeMachines(ctx context.Context) ([]*Machine, error) {
	clusters, err := c.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Machine
	for _, cl := range clusters {
		ns, err := c.nodes(ctx, cl.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ns...)
	}
	return out, nil
}

// CreateCluster starts the cluster and answers with it as GKE now shows it:
// PROVISIONING, with its seed pool declared. GKE creates control plane and pool
// in the one operation, so there is nothing to follow up.
func (c *GKEClient) CreateCluster(ctx context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error) {
	_, err := c.svc.Projects.Locations.Clusters.Create(c.parent(),
		&container.CreateClusterRequest{Cluster: buildGKECluster(spec, tags)}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gke: create cluster %s: %w", spec.Name, err)
	}
	cl, err := c.get(ctx, spec.Name)
	if err != nil {
		return nil, err
	}
	return clusterFromGKE(cl), nil
}

// DeleteCluster removes the cluster; GKE takes its node pools with it.
func (c *GKEClient) DeleteCluster(ctx context.Context, id string) error {
	if _, err := c.svc.Projects.Locations.Clusters.Delete(c.cluster(id)).Context(ctx).Do(); err != nil {
		return fmt.Errorf("gke: delete cluster %s: %w", id, err)
	}
	return nil
}

// ---- pools ----

func (c *GKEClient) getPool(ctx context.Context, clusterID, poolID string) (*NodePool, error) {
	p, err := c.svc.Projects.Locations.Clusters.NodePools.Get(c.pool(clusterID, poolID)).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gke: get node pool %s/%s: %w", clusterID, poolID, err)
	}
	return poolFromGKE(p), nil
}

func (c *GKEClient) CreateNodePool(ctx context.Context, clusterID string, spec *CreateNodePoolSpec) (*NodePool, error) {
	_, err := c.svc.Projects.Locations.Clusters.NodePools.Create(c.cluster(clusterID),
		&container.CreateNodePoolRequest{NodePool: buildGKEPool(spec)}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gke: create node pool %s/%s: %w", clusterID, spec.Name, err)
	}
	return c.getPool(ctx, clusterID, spec.Name)
}

// ScaleNodePool sets the pool's size. The resize is asynchronous upstream, so
// the pool returned carries the count asked for rather than the one still showing.
func (c *GKEClient) ScaleNodePool(ctx context.Context, clusterID, poolID string, count int) (*NodePool, error) {
	_, err := c.svc.Projects.Locations.Clusters.NodePools.SetSize(c.pool(clusterID, poolID),
		&container.SetNodePoolSizeRequest{NodeCount: int64(count), ForceSendFields: []string{"NodeCount"}}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gke: scale node pool %s/%s: %w", clusterID, poolID, err)
	}
	np, err := c.getPool(ctx, clusterID, poolID)
	if err != nil {
		return nil, err
	}
	np.Count = count
	return np, nil
}

func (c *GKEClient) DeleteNodePool(ctx context.Context, clusterID, poolID string) error {
	if _, err := c.svc.Projects.Locations.Clusters.NodePools.Delete(c.pool(clusterID, poolID)).Context(ctx).Do(); err != nil {
		return fmt.Errorf("gke: delete node pool %s/%s: %w", clusterID, poolID, err)
	}
	return nil
}

// ---- credentials ----

// GetCredentials is the cluster's endpoint and CA from its own record, and the
// service account's OAuth bearer, which GKE's apiserver accepts directly. The
// bearer lives about an hour and the token source renews it.
func (c *GKEClient) GetCredentials(ctx context.Context, clusterID string) (*ClusterCredentials, error) {
	if c.ts == nil {
		return nil, fmt.Errorf("gke: a carried account cannot hand out an apiserver token: the bearer is attached by the carrier and is not here")
	}
	cl, err := c.get(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if cl.Endpoint == "" || cl.MasterAuth == nil {
		return nil, fmt.Errorf("gke: cluster %s has no endpoint yet (%s)", clusterID, cl.Status)
	}
	ca, err := base64.StdEncoding.DecodeString(cl.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("gke: cluster %s certificate authority: %w", clusterID, err)
	}
	tok, err := c.ts.Token()
	if err != nil {
		return nil, fmt.Errorf("gke: token for cluster %s: %w", clusterID, err)
	}
	return &ClusterCredentials{
		Endpoint: "https://" + cl.Endpoint,
		CAData:   ca,
		Token:    tok.AccessToken,
		Expiry:   tok.Expiry,
	}, nil
}
