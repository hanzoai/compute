// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/container/v1"
	"google.golang.org/api/option"
)

// fakeGKE is one project at one location, served over HTTP the way the
// container API is, so the real client library does the encoding.
type fakeGKE struct {
	clusters map[string]*container.Cluster
	pools    map[string]*container.NodePool // cluster/name
	posted   map[string]json.RawMessage     // path -> last body
	deleted  []string
}

func newFakeGKE() *fakeGKE {
	return &fakeGKE{clusters: map[string]*container.Cluster{}, pools: map[string]*container.NodePool{}, posted: map[string]json.RawMessage{}}
}

const gkeBase = "/v1/projects/p-1/locations/us-central1/clusters"

func (f *fakeGKE) handler(w http.ResponseWriter, r *http.Request) {
	reply := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	rest := strings.TrimPrefix(r.URL.Path, gkeBase)
	switch {
	// The compute API's cross-zone instance list, filtered by the label GKE
	// puts on a cluster's nodes.
	case r.Method == http.MethodGet && r.URL.Path == "/projects/p-1/aggregated/instances":
		if r.URL.Query().Get("filter") != "labels.goog-k8s-cluster-name=acme-prod" {
			http.Error(w, "nodes asked for without the cluster filter: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		reply(map[string]any{"items": map[string]any{
			"zones/us-central1-a": map[string]any{"instances": []map[string]any{{
				"name": "gke-acme-prod-pool-1", "id": "77", "zone": "us-central1-a", "status": "RUNNING",
				"machineType": "e2-standard-4", "networkInterfaces": []map[string]any{{"networkIP": "10.0.0.7"}},
			}}},
			"zones/us-central1-b": map[string]any{},
		}})
	case r.Method == http.MethodGet && rest == "":
		out := &container.ListClustersResponse{}
		for _, c := range f.clusters {
			out.Clusters = append(out.Clusters, c)
		}
		reply(out)
	case r.Method == http.MethodPost && rest == "":
		var req container.CreateClusterRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		body, _ := json.Marshal(req)
		f.posted[r.URL.Path] = body
		req.Cluster.Location, req.Cluster.Status = "us-central1", "PROVISIONING"
		f.clusters[req.Cluster.Name] = req.Cluster
		reply(&container.Operation{Name: "op-1"})
	case r.Method == http.MethodGet && strings.Count(rest, "/") == 1:
		c, ok := f.clusters[rest[1:]]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
			return
		}
		reply(c)
	case r.Method == http.MethodDelete && strings.Count(rest, "/") == 1:
		f.deleted = append(f.deleted, rest[1:])
		reply(&container.Operation{Name: "op-2"})
	case r.Method == http.MethodPost && strings.HasSuffix(rest, "/nodePools"):
		cluster := strings.TrimSuffix(rest[1:], "/nodePools")
		var req container.CreateNodePoolRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		body, _ := json.Marshal(req)
		f.posted[r.URL.Path] = body
		f.pools[cluster+"/"+req.NodePool.Name] = req.NodePool
		reply(&container.Operation{Name: "op-3"})
	case r.Method == http.MethodPost && strings.HasSuffix(rest, ":setSize"):
		b, _ := json.Marshal(json.RawMessage(readAll(r)))
		f.posted[r.URL.Path] = b
		reply(&container.Operation{Name: "op-4"})
	case r.Method == http.MethodGet && strings.Contains(rest, "/nodePools/"):
		key := strings.Replace(rest[1:], "/nodePools/", "/", 1)
		p, ok := f.pools[key]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
			return
		}
		reply(p)
	case r.Method == http.MethodDelete && strings.Contains(rest, "/nodePools/"):
		f.deleted = append(f.deleted, rest[1:])
		reply(&container.Operation{Name: "op-5"})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func readAll(r *http.Request) []byte {
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := r.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			return []byte(buf.String())
		}
	}
}

func gkeUnderTest(t *testing.T, f *fakeGKE) *GKEClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	svc, err := container.NewService(context.Background(), option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return &GKEClient{
		svc: svc, project: "p-1", location: "us-central1",
		vm:  MachineGcpClient{httpClient: srv.Client(), projectID: "p-1", compute: srv.URL},
		ts:  oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "ya29.fake", Expiry: time.Now().Add(time.Hour)}),
	}
}

// ---- pure mappers ----

func TestGKEClusterRequest(t *testing.T) {
	spec := &CreateClusterSpec{Name: "acme-prod", Version: "1.31", NodePool: CreateClusterNodePool{Size: "e2-standard-4", Count: 0}}
	cl := buildGKECluster(spec, []string{"managed-by:hanzo-visor", "hanzo-org:acme"})
	if cl.Name != "acme-prod" || cl.InitialClusterVersion != "1.31" || cl.ResourceLabels["hanzo-org"] != "acme" {
		t.Fatalf("identity mapping wrong: %+v", cl)
	}
	if len(cl.NodePools) != 1 {
		t.Fatalf("want one seed pool, got %d", len(cl.NodePools))
	}
	p := cl.NodePools[0]
	if p.Name != "acme-prod-pool" || p.Config.MachineType != "e2-standard-4" || p.InitialNodeCount != 1 {
		t.Fatalf("seed pool = %+v (count floors at 1)", p)
	}

	auto := buildGKEPool(&CreateNodePoolSpec{Name: "gpu", Size: "a2-highgpu-1g", Count: 0, AutoScale: true, MinNodes: 0, MaxNodes: 4})
	body, _ := json.Marshal(auto)
	if !strings.Contains(string(body), `"initialNodeCount":0`) {
		t.Fatalf("a zero count must be SENT — omitted, GKE gives the pool three nodes: %s", body)
	}
	if !auto.Autoscaling.Enabled || auto.Autoscaling.MaxNodeCount != 4 {
		t.Fatalf("autoscaling = %+v", auto.Autoscaling)
	}

	np := poolFromGKE(auto)
	if !np.AutoScale || np.MaxNodes != 4 || np.Size != "a2-highgpu-1g" || np.ID != "gpu" {
		t.Fatalf("poolFromGKE = %+v", np)
	}
}

// ---- lifecycle over the wire ----

func TestGKECreateAndGet(t *testing.T) {
	f := newFakeGKE()
	c := gkeUnderTest(t, f)
	spec := &CreateClusterSpec{Name: "acme-prod", NodePool: CreateClusterNodePool{Size: "e2-standard-4", Count: 3}}
	kc, err := c.CreateCluster(context.Background(), spec, []string{"managed-by:hanzo-visor", orgTag("acme")})
	if err != nil {
		t.Fatal(err)
	}
	if kc.ID != "acme-prod" || kc.Status != "PROVISIONING" || kc.RegionSlug != "us-central1" || kc.Provider != providerGCP {
		t.Fatalf("created = %+v", kc)
	}
	if !clusterHasTag(kc.Tags, orgTag("acme")) {
		t.Fatalf("owner lost through resource labels: %v", kc.Tags)
	}
	var req container.CreateClusterRequest
	if err := json.Unmarshal(f.posted[gkeBase], &req); err != nil {
		t.Fatal(err)
	}
	if req.Cluster.NodePools[0].InitialNodeCount != 3 || req.Cluster.NodePools[0].Config.MachineType != "e2-standard-4" {
		t.Fatalf("posted %s", f.posted[gkeBase])
	}

	d, err := c.GetCluster(context.Background(), "acme-prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.NodePools) != 1 || d.NodePools[0].Name != "acme-prod-pool" || d.NodePools[0].Count != 3 {
		t.Fatalf("pools = %+v", d.NodePools)
	}
	if len(d.Nodes) != 1 || d.Nodes[0].Id != "77" || d.Nodes[0].Provider != providerGCP || d.Nodes[0].PrivateIp != "10.0.0.7" || d.Nodes[0].Tag != "gke-cluster:acme-prod" {
		t.Fatalf("nodes = %+v", d.Nodes)
	}
	if _, err := c.get(context.Background(), "nope"); !IsNotFound(err) {
		t.Fatalf("a missing cluster must read as not found, got %v", err)
	}
	if err := c.DeleteCluster(context.Background(), "acme-prod"); err != nil || f.deleted[0] != "acme-prod" {
		t.Fatalf("delete: %v %v", err, f.deleted)
	}
}

func TestGKEPools(t *testing.T) {
	f := newFakeGKE()
	f.clusters["acme-prod"] = &container.Cluster{Name: "acme-prod", Location: "us-central1", Status: "RUNNING"}
	c := gkeUnderTest(t, f)

	np, err := c.CreateNodePool(context.Background(), "acme-prod", &CreateNodePoolSpec{Name: "gpu", Size: "a2-highgpu-1g", Count: 2, Labels: map[string]string{"tier": "gpu"}})
	if err != nil {
		t.Fatal(err)
	}
	if np.ID != "gpu" || np.Size != "a2-highgpu-1g" || np.Count != 2 || np.Labels["tier"] != "gpu" {
		t.Fatalf("pool = %+v", np)
	}

	np, err = c.ScaleNodePool(context.Background(), "acme-prod", "gpu", 0)
	if err != nil {
		t.Fatal(err)
	}
	if np.Count != 0 {
		t.Fatalf("scaled pool count = %d", np.Count)
	}
	if body := string(f.posted[gkeBase+"/acme-prod/nodePools/gpu:setSize"]); !strings.Contains(body, `"nodeCount":0`) {
		t.Fatalf("a scale to zero must SEND zero, got %s", body)
	}
	if err := c.DeleteNodePool(context.Background(), "acme-prod", "gpu"); err != nil || f.deleted[0] != "acme-prod/nodePools/gpu" {
		t.Fatalf("delete pool: %v %v", err, f.deleted)
	}
}

func TestGKECredentials(t *testing.T) {
	f := newFakeGKE()
	ca := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	f.clusters["acme-prod"] = &container.Cluster{
		Name: "acme-prod", Location: "us-central1", Status: "RUNNING", Endpoint: "34.1.2.3",
		MasterAuth: &container.MasterAuth{ClusterCaCertificate: base64.StdEncoding.EncodeToString([]byte(ca))},
	}
	c := gkeUnderTest(t, f)
	creds, err := c.GetCredentials(context.Background(), "acme-prod")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Endpoint != "https://34.1.2.3" || string(creds.CAData) != ca || creds.Token != "ya29.fake" {
		t.Fatalf("creds = %+v", creds)
	}
	if creds.Expiry.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("expiry %v is not the token's", creds.Expiry)
	}

	c.ts = nil
	if _, err := c.GetCredentials(context.Background(), "acme-prod"); err == nil || !strings.Contains(err.Error(), "carried") {
		t.Fatalf("a carried account has no bearer to hand out, got %v", err)
	}
}

// ---- the registry ----

const fakeServiceAccount = `{"type":"service_account","project_id":"p-1","client_email":"compute@p-1.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n","token_uri":"https://oauth2.googleapis.com/token"}`

func TestGCPSpeaksKubernetesAndIsCarried(t *testing.T) {
	RegisterCarrier(nil)
	c, err := newMachineGcpClient("p-1", fakeServiceAccount, "us-central1", directHTTP())
	if err != nil {
		t.Fatal(err)
	}
	k, ok := kubernetesFor(c)
	if !ok || k.Provider() != providerGCP {
		t.Fatalf("Google Cloud must speak Kubernetes: %v %v", ok, k)
	}
	if k.(*GKEClient).ts == nil {
		t.Fatal("with a service account the bearer is minted here")
	}

	t.Cleanup(func() { RegisterCarrier(nil) })
	var saw Credential
	RegisterCarrier(func(c Credential) (*http.Client, error) { saw = c; return &http.Client{}, nil })
	mc, err := NewMachineClient(Credential{Provider: providerGCP, KeyID: "p-1", Region: "us-central1"})
	if err != nil {
		t.Fatalf("carried Google Cloud must build: %v", err)
	}
	if saw.Provider != providerGCP {
		t.Fatalf("carrier consulted for %q", saw.Provider)
	}
	if mc.(KubernetesCapable).Kubernetes().(*GKEClient).ts != nil {
		t.Fatal("carried, the bearer is egress's and must not be minted here")
	}
}
