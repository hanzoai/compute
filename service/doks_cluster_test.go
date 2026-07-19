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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/digitalocean/godo"
)

// ---- pure mappers (no client) ----

// buildClusterCreateRequest is the spec->godo contract the create surface rides.
// This proves the mapping AND the ergonomic defaults without touching DigitalOcean.
func TestBuildClusterCreateRequest(t *testing.T) {
	spec := &CreateClusterSpec{
		Name:    "acme-prod",
		Region:  "nyc3",
		Version: "1.31.1-do.4",
		NodePool: CreateClusterNodePool{
			Name:  "workers",
			Size:  "s-4vcpu-8gb",
			Count: 3,
		},
	}
	tags := []string{"managed-by:hanzo-visor", "hanzo-org:acme"}
	req := buildClusterCreateRequest(spec, tags)

	if req.Name != "acme-prod" || req.RegionSlug != "nyc3" || req.VersionSlug != "1.31.1-do.4" {
		t.Fatalf("identity mapping wrong: %+v", req)
	}
	if len(req.Tags) != 2 || req.Tags[1] != "hanzo-org:acme" {
		t.Fatalf("ownership tags not propagated: %v", req.Tags)
	}
	if len(req.NodePools) != 1 {
		t.Fatalf("want exactly one seed node pool, got %d", len(req.NodePools))
	}
	p := req.NodePools[0]
	if p.Name != "workers" || p.Size != "s-4vcpu-8gb" || p.Count != 3 {
		t.Fatalf("node pool mapping wrong: %+v", p)
	}
}

// Ergonomic defaults: empty version -> "latest", unnamed pool -> "<cluster>-pool",
// a non-positive count floors at 1 (a cluster must have at least one worker).
func TestBuildClusterCreateRequestDefaults(t *testing.T) {
	req := buildClusterCreateRequest(&CreateClusterSpec{
		Name:     "edge",
		Region:   "sfo3",
		NodePool: CreateClusterNodePool{Size: "s-2vcpu-4gb", Count: 0},
	}, nil)
	if req.VersionSlug != "latest" {
		t.Fatalf("empty version must default to latest, got %q", req.VersionSlug)
	}
	if got := req.NodePools[0].Name; got != "edge-pool" {
		t.Fatalf("unnamed pool must default to <cluster>-pool, got %q", got)
	}
	if got := req.NodePools[0].Count; got != 1 {
		t.Fatalf("non-positive count must floor at 1, got %d", got)
	}
}

// clusterDetailFromGodo expands an authoritative cluster Get into the detail shape:
// identity + pools + worker nodes as fleet Machines. A node still provisioning (no
// droplet_id) is skipped — no droplet, no fleet identity.
func TestClusterDetailFromGodo(t *testing.T) {
	gc := &godo.KubernetesCluster{
		ID:         "cl-1",
		Name:       "acme-prod",
		RegionSlug: "nyc3",
		Tags:       []string{"hanzo-org:acme"},
		Status:     &godo.KubernetesClusterStatus{State: godo.KubernetesClusterStatusRunning},
		NodePools: []*godo.KubernetesNodePool{{
			ID:    "pool-1",
			Name:  "workers",
			Size:  "s-4vcpu-8gb",
			Count: 2,
			Nodes: []*godo.KubernetesNode{
				{ID: "n1", Name: "worker-1", DropletID: "555", Status: &godo.KubernetesNodeStatus{State: "running"}},
				{ID: "n2", Name: "worker-2", DropletID: "", Status: &godo.KubernetesNodeStatus{State: "provisioning"}},
			},
		}},
	}

	detail := clusterDetailFromGodo(gc)
	if detail.ID != "cl-1" || detail.Name != "acme-prod" || detail.RegionSlug != "nyc3" || detail.Status != "running" {
		t.Fatalf("cluster identity flatten wrong: %+v", detail.KubernetesCluster)
	}
	if len(detail.NodePools) != 1 || detail.NodePools[0].Size != "s-4vcpu-8gb" {
		t.Fatalf("node pools not mapped: %+v", detail.NodePools)
	}
	if len(detail.Nodes) != 1 {
		t.Fatalf("provisioning node (no droplet) must be skipped: want 1 machine, got %d", len(detail.Nodes))
	}
	m := detail.Nodes[0]
	if m.Id != "555" || m.Size != "s-4vcpu-8gb" || m.Region != "nyc3" || m.State != "running" {
		t.Fatalf("worker machine mapping wrong: %+v", m)
	}
	if m.Tag != "doks-cluster:acme-prod" {
		t.Fatalf("worker must carry its cluster tag, got %q", m.Tag)
	}
}

// ---- client round-trips against a mocked DigitalOcean API (no real DO) ----

// doksTestServer emulates the DO managed-Kubernetes endpoints this package uses.
// clusters is keyed by id; List returns them all, Get returns one, Create records
// the decoded request + echoes a created cluster, Delete 204s (or 404s an unknown).
type doksTestServer struct {
	clusters map[string]*godo.KubernetesCluster
	created  *godo.KubernetesClusterCreateRequest
}

func newDOKSTestClient(t *testing.T, srv *doksTestServer) *DOKSClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/kubernetes/clusters", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			list := make([]*godo.KubernetesCluster, 0, len(srv.clusters))
			for _, c := range srv.clusters {
				list = append(list, c)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kubernetes_clusters": list})
		case http.MethodPost:
			var req godo.KubernetesClusterCreateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			srv.created = &req
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"kubernetes_cluster": &godo.KubernetesCluster{
				ID: "cl-new", Name: req.Name, RegionSlug: req.RegionSlug, Tags: req.Tags,
				Status: &godo.KubernetesClusterStatus{State: godo.KubernetesClusterStatusProvisioning},
			}})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	// Trailing-path handler for /clusters/{id} (Get + Delete).
	mux.HandleFunc("/v2/kubernetes/clusters/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/v2/kubernetes/clusters/"):]
		c, ok := srv.clusters[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "not_found", "message": "cluster not found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"kubernetes_cluster": c})
		case http.MethodDelete:
			delete(srv.clusters, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	gc := godo.NewClient(nil)
	u, _ := url.Parse(ts.URL + "/")
	gc.BaseURL = u
	return &DOKSClient{Client: gc}
}

// ListClusters returns every cluster; clustersByTag scopes to one org's tag — the
// tenant-isolation filter that keeps one org from ever listing another's clusters.
func TestListClustersAndTagScoping(t *testing.T) {
	srv := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{
		"cl-acme": {ID: "cl-acme", Name: "acme", RegionSlug: "nyc3", Tags: []string{"hanzo-org:acme"},
			Status: &godo.KubernetesClusterStatus{State: godo.KubernetesClusterStatusRunning}},
		"cl-other": {ID: "cl-other", Name: "other", RegionSlug: "sfo3", Tags: []string{"hanzo-org:other"},
			Status: &godo.KubernetesClusterStatus{State: godo.KubernetesClusterStatusRunning}},
	}}
	c := newDOKSTestClient(t, srv)

	all, err := c.ListClusters(context.Background())
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 clusters account-wide, got %d", len(all))
	}

	scoped, err := clustersByTag(context.Background(), c.Client, orgTag("acme"))
	if err != nil {
		t.Fatalf("clustersByTag: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Name != "acme" {
		t.Fatalf("tag scoping must return only acme's cluster, got %+v", scoped)
	}
}

// GetCluster returns detail (pools + worker nodes) from an authoritative Get.
func TestGetClusterDetail(t *testing.T) {
	srv := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{
		"cl-1": {ID: "cl-1", Name: "acme", RegionSlug: "nyc3", Tags: []string{"hanzo-org:acme"},
			Status: &godo.KubernetesClusterStatus{State: godo.KubernetesClusterStatusRunning},
			NodePools: []*godo.KubernetesNodePool{{
				ID: "p1", Name: "workers", Size: "s-4vcpu-8gb", Count: 1,
				Nodes: []*godo.KubernetesNode{{ID: "n1", Name: "w1", DropletID: "777", Status: &godo.KubernetesNodeStatus{State: "running"}}},
			}},
		},
	}}
	c := newDOKSTestClient(t, srv)

	detail, err := c.GetCluster(context.Background(), "cl-1")
	if err != nil {
		t.Fatalf("GetCluster: %v", err)
	}
	if detail.Name != "acme" || len(detail.NodePools) != 1 || len(detail.Nodes) != 1 {
		t.Fatalf("detail shape wrong: %+v", detail)
	}
	if detail.Nodes[0].Id != "777" {
		t.Fatalf("worker machine id must be droplet id, got %q", detail.Nodes[0].Id)
	}
}

// CreateCluster maps the spec (+ ownership tags) into the DO request and returns
// the created cluster. The mock records the decoded request so we assert the wire.
func TestCreateClusterRoundTrip(t *testing.T) {
	srv := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{}}
	c := newDOKSTestClient(t, srv)

	spec := &CreateClusterSpec{Name: "acme-prod", Region: "nyc3", Version: "latest",
		NodePool: CreateClusterNodePool{Size: "s-4vcpu-8gb", Count: 2}}
	tags := []string{"managed-by:hanzo-visor", "hanzo-org:acme"}

	cluster, err := c.CreateCluster(context.Background(), spec, tags)
	if err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if cluster.Name != "acme-prod" || cluster.Status != "provisioning" {
		t.Fatalf("created cluster shape wrong: %+v", cluster)
	}
	if srv.created == nil {
		t.Fatal("server never received a create request")
	}
	if srv.created.RegionSlug != "nyc3" || srv.created.VersionSlug != "latest" {
		t.Fatalf("create request placement wrong: %+v", srv.created)
	}
	if len(srv.created.Tags) != 2 || srv.created.Tags[1] != "hanzo-org:acme" {
		t.Fatalf("ownership tag must reach DO: %v", srv.created.Tags)
	}
	if len(srv.created.NodePools) != 1 || srv.created.NodePools[0].Count != 2 {
		t.Fatalf("seed pool must reach DO: %+v", srv.created.NodePools)
	}
}

// DeleteCluster removes an existing cluster; deleting an unknown one surfaces a DO
// 404 that IsNotFound classifies as already-gone (idempotent-delete success).
func TestDeleteClusterRoundTrip(t *testing.T) {
	srv := &doksTestServer{clusters: map[string]*godo.KubernetesCluster{
		"cl-1": {ID: "cl-1", Name: "acme", Tags: []string{"hanzo-org:acme"}},
	}}
	c := newDOKSTestClient(t, srv)

	if err := c.DeleteCluster(context.Background(), "cl-1"); err != nil {
		t.Fatalf("DeleteCluster: %v", err)
	}
	if _, ok := srv.clusters["cl-1"]; ok {
		t.Fatal("cluster was not deleted")
	}

	err := c.DeleteCluster(context.Background(), "cl-1")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("deleting an absent cluster must surface an IsNotFound error, got %v", err)
	}
}
