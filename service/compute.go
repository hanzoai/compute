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

// Package service — compute.go is Hanzo's resell compute surface over a single
// PLATFORM DigitalOcean account. It is distinct from the per-owner "bring your own
// cloud" Provider path (machine_cloud.go): here ONE Hanzo DO token (from KMS)
// backs every tenant, and droplets are namespaced by an org tag so list/get/
// delete are scoped to the caller's org at the DigitalOcean layer — never the
// whole account. The catalog (regions/sizes/GPUs) is fetched once and cached
// so the dashboard is fast and DO is not hammered.
package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/godo"
	"github.com/hanzoai/visor/logs"
)

// orgTagKey/orgTag namespace droplets by the Hanzo org that owns them. Per-org
// isolation is enforced by querying DigitalOcean with this exact tag, so one
// tenant can never enumerate another's machines.
const orgTagKey = "hanzo-org"

func orgTag(org string) string { return orgTagKey + ":" + org }

// projectTag namespaces a droplet by the project (WITHIN its org) that owns it,
// using the same "key:value" tag shape as orgTag. projectTagKey ("hanzo-project")
// is defined in analytics.go — the ONE tenant-hierarchy tag vocabulary shared by
// billing, metering and analytics. Only written for a NAMED project; the default
// (empty) project writes no project tag.
func projectTag(project string) string { return projectTagKey + ":" + project }

// newDigitalOceanClient builds a DigitalOcean client for Hanzo's own account.
//
// There is no token here and no way to supply one. The credential belongs to
// hanzoai/egress, which holds it on a host that is not a resource of the cloud
// it holds keys for; this process describes the call and egress attaches the
// key. So the client is the carrier's, and without a carrier there is nothing
// to build — refusing is the only honest answer, and it is a clean one.
func newDigitalOceanClient() (MachineDigitalOceanClient, error) {
	if !carrierRegistered() {
		return MachineDigitalOceanClient{}, fmt.Errorf("hanzo compute is not configured: set egressAddress — a provider key is spent through egress, never held here")
	}
	hc, err := httpFor(Credential{Provider: providerDigitalOcean})
	if err != nil {
		return MachineDigitalOceanClient{}, err
	}
	return newMachineDigitalOceanClient("", "", "", hc)
}

// ComputeConfigured reports whether this process can reach the platform account
// at all, so callers return a clean 503 instead of a cryptic client error.
//
// It is one question now — is there a carrier — because there is one way to
// reach a cloud. It was two while a token could also be held here, and the token
// half was the misleading one: a revoked key is still a non-empty string, so it
// answered "configured" through a revocation and every caller took the
// configured branch and failed inside it, reporting zeros that read as real data
// instead of "not connected". See ComputeReachable for the live question.
func ComputeConfigured() bool { return carrierRegistered() }

// ComputeReachable proves the provider credential actually works, by spending one
// authenticated round trip on it rather than inspecting its length.
//
// It exists because presence is not reachability. `token != ""` cannot tell a
// live token from a revoked one, so every caller that gated on ComputeConfigured
// took the configured branch and failed INSIDE it — reporting zeros that read as
// real data instead of "not connected". The hourly money sweep is where that is
// most expensive, so the sweep asks this question before it commits to an hour.
//
// The two answers a caller must tell apart:
//
//	nil   — either the configured cloud account has nothing to ask (no token configured, so
//	        there are no platform resources at all and an empty answer is the TRUE
//	        one), or the provider answered. Both mean: proceed.
//	error — a credential IS configured and did not work. Nothing about the platform
//	        account can be known this hour.
//
// That distinction is the whole point and it is the same one livePools already
// draws one level down: "there is nothing to ask" and "the answer did not come
// back" are different facts, and only the second is an error.
//
// Account.Get is the cheapest call that proves the credential itself: O(1), no
// paging, and it is what a revoked token 401s on. The client is built through the
// one constructor, so this inherits the 30s bound every other DigitalOcean call
// gets and cannot wedge the caller.
func ComputeReachable(ctx context.Context) error {
	if !ComputeConfigured() {
		return nil // nothing to ask
	}
	client, err := newDigitalOceanClient()
	if err != nil {
		return err
	}
	if _, _, err := client.Client.Account.Get(ctx); err != nil {
		return fmt.Errorf("configured cloud account unreachable: %w", err)
	}
	return nil
}

// ---- Catalog types (resellable) ----

// GPUSpec is the GPU detail for a GPU-backed size.
type GPUSpec struct {
	Count    int    `json:"count"`
	Model    string `json:"model"`
	Vram     int    `json:"vram"`
	VramUnit string `json:"vramUnit"`
}

// SizeInfo is a resellable compute size. Only Hanzo's resale price is exposed —
// the wholesale cost and the upstream provider are never surfaced (brand policy;
// margin stays private). Markup is applied once in pricing.go.
// DefaultLaunchSize is the size a launch gets when the caller names none — the
// tabs "New cloud machine" button, a bare CLI launch. It is a real DO slug so the
// quote the launch handler computes and the droplet the provider creates agree on
// one size. A tab runs `hanzo link` and a terminal; this is the smallest tier that
// comfortably does that.
const DefaultLaunchSize = "s-2vcpu-4gb"

type SizeInfo struct {
	Slug         string   `json:"slug"`
	Vcpus        int      `json:"vcpus"`
	MemoryMB     int      `json:"memoryMb"`
	DiskGB       int      `json:"diskGb"`
	Available    bool     `json:"available"`
	Regions      []string `json:"regions"`
	GPU          *GPUSpec `json:"gpu,omitempty"`
	Currency     string   `json:"currency"`
	PriceHourly  float64  `json:"priceHourly"`
	PriceMonthly float64  `json:"priceMonthly"`
}

// RegionInfo is a resellable region.
type RegionInfo struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Features  []string `json:"features"`
	Sizes     []string `json:"sizes"`
}

func isGPUSize(s godo.Size) bool {
	return s.GPUInfo != nil || strings.HasPrefix(s.Slug, "gpu-")
}

func sizeInfoFromDO(s godo.Size) SizeInfo {
	gpu := isGPUSize(s)
	si := SizeInfo{
		Slug:         s.Slug,
		Vcpus:        s.Vcpus,
		MemoryMB:     s.Memory,
		DiskGB:       s.Disk,
		Available:    s.Available,
		Regions:      s.Regions,
		Currency:     "USD",
		PriceHourly:  HanzoPrice(s.PriceHourly, gpu),
		PriceMonthly: HanzoPrice(s.PriceMonthly, gpu),
	}
	if s.GPUInfo != nil {
		g := &GPUSpec{Count: s.GPUInfo.Count, Model: s.GPUInfo.Model}
		if s.GPUInfo.VRAM != nil {
			g.Vram = s.GPUInfo.VRAM.Amount
			g.VramUnit = s.GPUInfo.VRAM.Unit
		}
		si.GPU = g
	}
	return si
}

// ---- Catalog cache ----
//
// Regions and sizes change rarely, so the catalog is cached for a long TTL and
// refreshed lazily on first access after expiry. Live droplets are never cached
// (ListOrgMachines always hits DO) so machine state is always current.

const catalogTTL = 24 * time.Hour

type catalogCache struct {
	mu        sync.RWMutex
	regions   []RegionInfo
	sizes     []SizeInfo
	fetchedAt time.Time
}

var catalog catalogCache

func (c *catalogCache) refresh() error {
	client, err := newDigitalOceanClient()
	if err != nil {
		return err
	}
	ctx := context.Background()

	var regions []RegionInfo
	ropt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		list, resp, err := client.Client.Regions.List(ctx, ropt)
		if err != nil {
			return fmt.Errorf("list DigitalOcean regions: %w", err)
		}
		for _, r := range list {
			regions = append(regions, RegionInfo{
				Slug:      r.Slug,
				Name:      r.Name,
				Available: r.Available,
				Features:  r.Features,
				Sizes:     r.Sizes,
			})
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		ropt.Page++
	}

	var sizes []SizeInfo
	sopt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		list, resp, err := client.Client.Sizes.List(ctx, sopt)
		if err != nil {
			return fmt.Errorf("list DigitalOcean sizes: %w", err)
		}
		for _, s := range list {
			sizes = append(sizes, sizeInfoFromDO(s))
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		sopt.Page++
	}

	c.mu.Lock()
	c.regions = regions
	c.sizes = sizes
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *catalogCache) ensureFresh() error {
	c.mu.RLock()
	fresh := len(c.sizes) > 0 && time.Since(c.fetchedAt) < catalogTTL
	c.mu.RUnlock()
	if fresh {
		return nil
	}
	return c.refresh()
}

// ListRegions returns the cached DigitalOcean regions catalog.
func ListRegions() ([]RegionInfo, error) {
	if err := catalog.ensureFresh(); err != nil {
		return nil, err
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	out := make([]RegionInfo, len(catalog.regions))
	copy(out, catalog.regions)
	return out, nil
}

// ListSizes returns the cached, resale-priced sizes catalog.
func ListSizes() ([]SizeInfo, error) {
	if err := catalog.ensureFresh(); err != nil {
		return nil, err
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	out := make([]SizeInfo, len(catalog.sizes))
	copy(out, catalog.sizes)
	return out, nil
}

// ListGPUSizes returns only the GPU-backed sizes from the catalog.
func ListGPUSizes() ([]SizeInfo, error) {
	sizes, err := ListSizes()
	if err != nil {
		return nil, err
	}
	gpus := make([]SizeInfo, 0, 8)
	for _, s := range sizes {
		if s.GPU != nil || strings.HasPrefix(s.Slug, "gpu-") {
			gpus = append(gpus, s)
		}
	}
	return gpus, nil
}

// SizeBySlug returns the resale size for a slug, or nil if unknown. Used to
// price launch quotes.
func SizeBySlug(slug string) (*SizeInfo, error) {
	// EKS and GKE worker types resolve from the static managed-Kubernetes
	// supplement first — deterministically and without a network call — because
	// they are not DigitalOcean slugs and would otherwise miss the catalog and
	// price nothing.
	if si, ok := managedK8sSize(slug); ok {
		return &si, nil
	}
	sizes, err := ListSizes()
	if err != nil {
		return nil, err
	}
	for i := range sizes {
		if sizes[i].Slug == slug {
			s := sizes[i]
			return &s, nil
		}
	}
	return nil, nil
}

// ---- Org-scoped machine operations (configured cloud account) ----

// ListOrgMachines returns the droplets tagged for org — per-org isolation enforced
// at the DigitalOcean layer via an exact tag query — optionally narrowed to a
// single project.
//
// project scopes the result WITHIN the org: the empty project is the org's default
// and returns EVERY org machine (today's behavior — a machine launched before the
// project dimension carries no hanzo-project tag), while a named project returns
// only the machines carrying that hanzo-project tag. Project is an attribution and
// view dimension, not a second isolation boundary — org is the tenant boundary, so
// get/delete stay org-scoped and only listing narrows by project.
func ListOrgMachines(org, project string) ([]*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	want := ""
	if project != "" {
		want = projectTag(project)
	}
	var machines []*Machine
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		droplets, resp, err := client.Client.Droplets.ListByTag(context.Background(), orgTag(org), opt)
		if err != nil {
			return nil, fmt.Errorf("list droplets for org %q: %w", org, err)
		}
		for _, d := range droplets {
			if want != "" && !dropletHasTag(d, want) {
				continue // narrow to the requested project
			}
			machines = append(machines, getMachineFromDroplet(d))
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}
	return machines, nil
}

// ListOrgKubernetesNodes returns one Machine per DOKS worker node for every
// cluster in Hanzo's PLATFORM DigitalOcean account tagged for org — the platform
// analogue of ListOrgMachines, but for managed-Kubernetes nodes. DOKS worker
// droplets carry k8s tags, not a hanzo-org DROPLET tag, so they never surface
// through ListOrgMachines; this lists them via the managed-Kubernetes API and maps
// each node to the same Machine shape. Per-org isolation is by the cluster's
// hanzo-org tag — a tenant can only ever see its own clusters' nodes.
func ListOrgKubernetesNodes(org string) ([]*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	return kubernetesNodeMachinesByTag(context.Background(), client.Client, orgTag(org))
}

// NewDOKSClientFromConfig builds a DOKS client on Hanzo's provider token. It is the
// cluster analogue of newDigitalOceanClient; per-org isolation is enforced by the
// callers via the hanzo-org tag, never by this client.
//
// clusterID is empty for ACCOUNT-level operations (list/get/create/delete), which
// address a cluster by id, and set for the pool operations bound to one cluster.
func NewDOKSClientFromConfig(clusterID string) (*DOKSClient, error) {
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	return &DOKSClient{Client: client.Client, ClusterID: clusterID}, nil
}

// LivePool is ONE node pool in the configured cloud account as the PROVIDER reports it
// right now — which cluster it belongs to, which org owns that cluster, its node
// slug, and how many nodes it is ACTUALLY running.
//
// This is the billable unit of a platform cluster, and the provider is its author.
// The stored row is a cache of it: useful for the rate the org was authorized at
// and the project it belongs to, and authoritative for neither existence nor
// count.
type LivePool struct {
	// Org owns the cluster, recovered from its hanzo-org tag. Empty means the
	// cluster is unattributable and nothing may be billed for it.
	Org string
	// ClusterID and PoolID are the upstream identity — the pair no customer can
	// rename, delete, or collide with another tenant's.
	ClusterID string
	PoolID    string
	// Name is the pool's upstream name. It addresses the cached row; it never
	// identifies the pool.
	Name string
	Size string
	// Nodes is the LIVE node count, so an autoscaled pool bills what it grew to.
	Nodes int
	// Created is the owning cluster's creation time (RFC3339), the fallback
	// launch-hour marker for a pool with no stored row.
	Created string
}

// ListLivePools returns every node pool of every cluster in Hanzo's platform
// account, with the org that owns it and its LIVE node count — the authoritative
// answer to "what is this account running, for whom, and how much of it".
//
// It is the node-pool analogue of ListMeteredMachines and shares the ONE
// cluster enumeration (listClustersFull) with the cluster and node listers, so a
// pool's identity is sourced identically wherever it surfaces.
//
// A cluster with no hanzo-org tag yields pools with an empty Org: they are
// returned rather than dropped, so the sweep can report them as unattributable
// instead of silently running an untagged cluster for free.
func ListLivePools(ctx context.Context) ([]LivePool, error) {
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	clusters, err := listClustersFull(ctx, client.Client)
	if err != nil {
		return nil, err
	}
	var out []LivePool
	for _, gc := range clusters {
		org := orgFromClusterTags(gc.Tags)
		created := ""
		if !gc.CreatedAt.IsZero() {
			created = gc.CreatedAt.UTC().Format(time.RFC3339)
		}
		for _, p := range poolsFromGodo(gc.NodePools) {
			out = append(out, LivePool{
				Org: org, ClusterID: gc.ID, PoolID: p.ID, Name: p.Name,
				Size: p.Size, Nodes: liveNodes(p), Created: created,
			})
		}
	}
	return out, nil
}

// orgFromClusterTags recovers the owning org from a cluster's tag LIST, through
// the SAME hanzo-org read-back the droplet path uses on its comma-joined string
// form. One parser, so a cluster and a droplet can never disagree about who owns
// them.
func orgFromClusterTags(tags []string) string {
	return orgFromTag(strings.Join(tags, ","))
}

// ListOrgKubernetesClusters returns every DOKS cluster in the configured cloud account
// tagged for org — the platform analogue of ListOrgMachines for whole clusters. Per-org
// isolation is by the cluster's hanzo-org tag: a tenant only ever sees its own
// clusters, never another org's.
func ListOrgKubernetesClusters(org string) ([]*KubernetesCluster, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	all, failed, err := listClustersAcross(context.Background())
	if err != nil {
		return nil, err
	}
	tag := orgTag(org)
	out := make([]*KubernetesCluster, 0, len(all))
	for _, k := range all {
		if clusterHasTag(k.Tags, tag) {
			out = append(out, k)
		}
	}
	// A cloud that did not answer costs its own rows, never the fleet. The list
	// cannot carry that (callers decode an array), so it is on GET /v1/k8s/providers.
	if len(failed) > 0 {
		logs.Warning("cloud providers degraded for org %s: %s", org, strings.Join(failed, "; "))
	}
	return out, nil
}

// GetOrgKubernetesCluster returns one platform cluster's detail (pools + worker nodes),
// but ONLY if it carries the caller org's hanzo-org tag. A cluster owned by another
// org — or a missing cluster — resolves to (nil, nil): the controller renders it as
// "not found", so a tenant can never read another tenant's cluster by guessing an id.
func GetOrgKubernetesCluster(org, id string) (*KubernetesClusterDetail, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	_, detail, err := findCluster(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, nil
	}
	if !clusterHasTag(detail.Tags, orgTag(org)) {
		return nil, nil // isolation: not this org's cluster
	}
	return detail, nil
}

// OrgKubernetesCredentials mints a short-lived credential for one of org's own
// clusters. Isolation is the SAME hanzo-org tag GetOrgKubernetesCluster reads —
// a cluster owned by another org, or no cluster at all, resolves to (nil, nil)
// and the controller renders "not found". Guessing an id must not hand anyone a
// token to somebody else's apiserver.
func OrgKubernetesCredentials(ctx context.Context, org, id string) (*ClusterCredentials, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	clients, _ := kubernetesClients()
	if len(clients) == 0 {
		return nil, fmt.Errorf("no cloud provider is configured")
	}
	return mintCredentials(ctx, clients, org, id)
}

// mintCredentials is the isolation itself: find the cluster across the clouds,
// and mint only if it carries org's tag. It is the twin of locate — same pairing,
// same reason, so the rule can be checked without a cloud registry.
func mintCredentials(ctx context.Context, clients []KubernetesClientInterface, org, id string) (*ClusterCredentials, error) {
	client, detail, err := locate(ctx, clients, id)
	if err != nil {
		return nil, err
	}
	if detail == nil || !clusterHasTag(detail.Tags, orgTag(org)) {
		return nil, nil
	}
	return client.GetCredentials(ctx, id)
}

// clusterCreator is the minimal cloud surface a metered cluster create needs. It
// is satisfied by *DOKSClient and by a test fake, which is what makes "refused
// requests provision NOTHING" a property a test can observe rather than a claim.
type clusterCreator interface {
	CreateCluster(ctx context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error)
}

// SeedPool is the node pool a cluster create provisions, in the terms the store
// needs to make it BILLABLE. A cluster's nodes are the cluster's whole cost, and
// the hourly sweep bills node-pool rows — so a cluster that provisions a pool and
// writes no row is billed its first hour by the provision path and then runs free
// forever. That is what this type exists to prevent.
type SeedPool struct {
	Org       string
	Project   string
	ClusterID string
	Name      string
	Size      string
	Count     int
	CentsHour int64
}

// recordSeed / forgetCluster are the node-pool store as the cluster provision path
// sees it: where a metered create writes its seed pool, and where a teardown
// clears what it wrote. They are plain functions because each call site depends on
// exactly one of them, and they are PARAMETERS because service cannot import
// object (object imports service) — handing the store in at the composition root
// is also what lets a test fake observe "a create wrote its billable row" and
// "a delete stopped the meter" instead of taking either on faith.
type recordSeed func(SeedPool) error

type forgetCluster func(org, clusterID string) error

// CreateOrgKubernetesCluster provisions a DOKS cluster in the configured cloud account for
// org, stamping it managed-by + hanzo-org:<org> so it associates to the tenant
// exactly like a droplet — which is what makes it visible to that org's cluster and
// node listers (and invisible to every other org).
//
// PLATFORM ACCOUNT means Hanzo pays the upstream bill for every node in the seed
// pool, so this goes through the money gate exactly like a droplet launch — and
// records the pool it provisioned, so the sweep keeps billing it.
func CreateOrgKubernetesCluster(org, project string, spec *CreateClusterSpec, record recordSeed) (*KubernetesCluster, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	if spec == nil {
		return nil, fmt.Errorf("spec is required")
	}
	client, err := backendFor(spec.Provider)
	if err != nil {
		return nil, err
	}
	kc, err := createClusterMetered(context.Background(), client, record, org, project, spec)
	if err != nil {
		return nil, err
	}
	if kc != nil && kc.Provider == "" {
		kc.Provider = client.Provider()
	}
	return kc, nil
}

// createClusterMetered is the ONE metered cluster provision: price the seed pool
// from the resale catalog, authorize the org for its first hour at that price,
// provision, PERSIST the pool as a billable row, then record the first hour.
// Fail-closed on the balance AND on the price — an org that cannot be authorized
// and a size that cannot be priced both provision nothing.
//
// The charge is the seed pool's FULL first hour (hourly × node count), read from
// the same seedPoolCount the upstream request is built with, so the quantity
// authorized is the quantity provisioned.
//
// Hour one is this function's. EVERY HOUR AFTER IS THE SWEEP'S, and the sweep
// reads node-pool rows — so the row is not bookkeeping, it IS the recurring bill.
// Without it a cluster was gated and debited exactly once and then ran free: it
// writes no droplet tag either, so neither meter could see it. The row is written
// BEFORE the debit, because a debit with no row is a cluster that bills once, and
// a row with no debit is one reconciled hour.
func createClusterMetered(ctx context.Context, client clusterCreator, record recordSeed, org, project string, spec *CreateClusterSpec) (*KubernetesCluster, error) {
	count := seedPoolCount(spec)
	hourly, err := HourlyCents(spec.NodePool.Size)
	if err != nil {
		return nil, err
	}
	var cluster *KubernetesCluster
	// A cluster's seed pool does not autoscale — CreateClusterSpec carries no
	// bounds — so the ceiling and the charge are the same count.
	err = Provision(ctx, org, project, hourly*int64(count), hourly*int64(count), spec.NodePool.Size, func() (string, error) {
		c, err := client.CreateCluster(ctx, spec, []string{"managed-by:hanzo-visor", orgTag(org)})
		if err != nil {
			return "", err
		}
		cluster = c
		if err := record(SeedPool{
			Org: org, Project: project, ClusterID: c.ID, Name: seedPoolName(spec),
			Size: spec.NodePool.Size, Count: count, CentsHour: hourly,
		}); err != nil {
			// The cluster is up and drawing upstream cost; refusing to return it
			// would leave the customer paying for something they were told they did
			// not get. But an unrecorded pool is an UNBILLED pool for every hour it
			// runs, so this is the loudest line in the file. Nothing reconciles it.
			logs.Warning("compute metering: cluster %s (org %s) provisioned but its seed pool was NOT recorded — it will be billed for its first hour only: %v", c.ID, org, err)
		}
		return "cluster-" + c.ID, nil
	})
	if err != nil {
		return nil, err
	}
	return cluster, nil
}

// DeleteOrgKubernetesCluster destroys a platform cluster by id, but ONLY if it carries
// the caller org's hanzo-org tag — the same isolation as GetOrgKubernetesCluster, so
// a tenant can never delete another tenant's cluster. An already-absent cluster is a
// no-op success (idempotent delete).
//
// The cluster's billable rows go with it. They are what the hourly sweep bills, so
// a row outliving its cluster is not stale data — it is an invoice for nodes that
// no longer exist. The meter is stopped even for an already-absent cluster, so a
// retry after a partial delete still closes the bill.
func DeleteOrgKubernetesCluster(org, id string, forget forgetCluster) error {
	if org == "" {
		return fmt.Errorf("org is required")
	}
	ctx := context.Background()
	client, detail, err := findCluster(ctx, id)
	if err != nil {
		return err
	}
	if detail == nil {
		// Gone on every backend. Stop billing it, same as the metered path does
		// for an upstream 404 — a cluster that no cloud has must not keep metering.
		stopClusterMeter(forget, org, id)
		return nil
	}
	return deleteClusterMetered(ctx, client, forget, org, id)
}

// clusterDestroyer is the minimal cloud surface a metered cluster teardown needs
// — the mirror of clusterCreator, and satisfied by the same *DOKSClient, so
// "a delete stops the meter" is observable against a fake exactly like
// "a refused create provisions nothing" is.
type clusterDestroyer interface {
	GetCluster(ctx context.Context, id string) (*KubernetesClusterDetail, error)
	DeleteCluster(ctx context.Context, id string) error
}

// deleteClusterMetered is the ONE metered cluster teardown: verify the cluster is
// this org's, destroy it, then stop its meter. A cluster already absent upstream
// still has its meter stopped, so a retry after a partial delete closes the bill.
//
// Clearing the rows is not tidiness. The hourly sweep bills node-pool rows, so a
// row that outlives its cluster is an invoice for nodes that no longer exist.
func deleteClusterMetered(ctx context.Context, client clusterDestroyer, forget forgetCluster, org, id string) error {
	detail, err := client.GetCluster(ctx, id)
	if err != nil {
		if IsNotFound(err) {
			stopClusterMeter(forget, org, id) // already gone upstream; stop billing it
			return nil
		}
		return err
	}
	if !clusterHasTag(detail.Tags, orgTag(org)) {
		return fmt.Errorf("cluster not found")
	}
	if err := client.DeleteCluster(ctx, id); err != nil {
		return err
	}
	stopClusterMeter(forget, org, id)
	return nil
}

// stopClusterMeter drops the cluster's billable rows. A failure is loud and never
// fails the delete — the cluster is gone either way, and the operator needs to
// know that a row is still metering nodes that no longer run.
func stopClusterMeter(forget forgetCluster, org, id string) {
	if err := forget(org, id); err != nil {
		logs.Warning("compute metering: cluster %s (org %s) deleted but its node-pool rows were NOT cleared — they will keep billing: %v", id, org, err)
	}
}

// kubernetesAutoTags are the BARE tags DigitalOcean puts on the droplets it
// creates as managed-Kubernetes node-pool workers. They are DO's, not ours: every
// node pool (and the workers it creates) is automatically tagged `k8s`,
// `k8s-worker` and `k8s:<cluster-id>`.
//
// Only the bare ones are listed, and that is what makes the guard safe rather
// than merely convenient: a client's launch tags always reach DigitalOcean as
// "key:value" (buildDropletTags formats every one of them with a colon), so a
// customer can produce `k8s:anything` but can NEVER produce the bare `k8s`. A
// prefix match here would hand every tenant a way to opt their own droplets out
// of the meter.
var kubernetesAutoTags = map[string]bool{"k8s": true, "k8s-worker": true}

// isKubernetesWorker reports whether a droplet is a managed-Kubernetes node-pool
// worker rather than a standalone resell machine.
func isKubernetesWorker(d godo.Droplet) bool {
	for _, t := range d.Tags {
		if kubernetesAutoTags[t] {
			return true
		}
	}
	return false
}

// dropletHasAnyOrgTag reports whether a droplet is attributed to SOME Hanzo org —
// the "is this a resell machine at all" question, as opposed to dropletHasOrgTag's
// "is it THIS org's".
func dropletHasAnyOrgTag(d godo.Droplet) bool {
	for _, t := range d.Tags {
		if strings.HasPrefix(t, orgTagKey+":") {
			return true
		}
	}
	return false
}

// billableDroplet reports whether a droplet in the configured cloud account is on
// the hourly MACHINE meter. It is the ONE answer to that question, kept pure and
// separate from the DigitalOcean enumeration so "exactly one meter per node" is a
// property a test can check rather than a claim about a loop.
//
// Three conditions, each excluding a different way of not being a billable resell
// machine:
//
//   - RUNNING. A stopped droplet consumes no compute-hour.
//
//   - NOT a managed-Kubernetes worker. This is the one that is easy to get wrong,
//     and getting it wrong bills the customer twice. A cluster's worker nodes ARE
//     droplets, and DigitalOcean propagates a cluster's tags to them — hanzo-org
//     among them, because that is the tag the cluster create stamps. So the same
//     node is reachable by two sweeps: this one, as a droplet, and the node-pool
//     sweep, as one node of its pool. The node-pool sweep is the meter of record
//     for a cluster's nodes — it is the one that knows the pool, its size and its
//     live count — so a Kubernetes worker is not a machine here.
//
//     Skipping it is now a property of VISOR, not of how DigitalOcean happens to
//     tag things: the guard holds whether or not the cluster tag propagates. It
//     used to rest on the unverified belief that worker droplets carry no
//     hanzo-org tag, which is a claim about somebody else's product.
//
//   - carries a hanzo-org tag. An untagged platform droplet is not a resell machine
//     and is never billed to a tenant.
func billableDroplet(d godo.Droplet) bool {
	if d.Status != "active" { // godo "active" == running
		return false
	}
	if isKubernetesWorker(d) {
		return false // the pool sweep bills this node, as part of its pool
	}
	return dropletHasAnyOrgTag(d)
}

// ListMeteredMachines returns every droplet in the configured cloud account that
// billableDroplet admits — the set the recurring hourly meter debits. It
// lists across ALL orgs (no per-org tag filter): the org is recovered per machine
// from its own tag, so ONE sweep meters every tenant's running machines.
func ListMeteredMachines() ([]*Machine, error) {
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	var machines []*Machine
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		droplets, resp, err := client.Client.Droplets.List(context.Background(), opt)
		if err != nil {
			return nil, fmt.Errorf("list platform droplets: %w", err)
		}
		for _, d := range droplets {
			if !billableDroplet(d) {
				continue
			}
			machines = append(machines, getMachineFromDroplet(d))
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}
	return machines, nil
}

// dropletHasTag reports whether a droplet carries an exact tag. It is the ONE
// tag-membership check, shared by the org-isolation guard and the project view
// filter, so both compare tags identically.
func dropletHasTag(d godo.Droplet, tag string) bool {
	for _, t := range d.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func dropletHasOrgTag(d *godo.Droplet, org string) bool {
	return dropletHasTag(*d, orgTag(org))
}

// GetOrgMachine returns a single machine only if it belongs to org; otherwise
// nil (no cross-tenant leak, even to a valid caller of another org).
func GetOrgMachine(org, id string) (*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	dropletID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid machine id: %s", id)
	}
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	d, _, err := client.Client.Droplets.Get(context.Background(), dropletID)
	if err != nil {
		return nil, err
	}
	if d == nil || !dropletHasOrgTag(d, org) {
		return nil, nil
	}
	return getMachineFromDroplet(*d), nil
}

// DeleteOrgMachine deletes a machine only after confirming it belongs to org.
func DeleteOrgMachine(org, id string) error {
	m, err := GetOrgMachine(org, id)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("machine %q not found for this org", id)
	}
	client, err := newDigitalOceanClient()
	if err != nil {
		return err
	}
	dropletID, _ := strconv.Atoi(id)
	if _, err := client.Client.Droplets.Delete(context.Background(), dropletID); err != nil {
		return fmt.Errorf("delete droplet %s: %w", id, err)
	}
	// Roll a destroyed event into the analytics datastore (best-effort; never
	// blocks or fails the delete). m is the ownership-checked machine, so its
	// size and app/project tags are available.
	EmitCompute(org, ComputeDestroyed, m, 0)
	return nil
}

// LaunchOrgMachine provisions a droplet in the configured cloud account, tagged so it
// is owned by org and attributed to project. Both attribution tags are injected
// here (never trusted from the client body) so the machine is always attributable
// to the right tenant AND project.
//
// org is validated as a clean slug first: it becomes BOTH the hanzo-org
// attribution tag (read back by the hourly meter) AND the commerce billing key,
// so a value carrying the meter's "," / ":" separators must never reach the tag.
// A validated IAM owner claim is already a DNS-label slug, so this only rejects a
// malformed/forged org — it never breaks a real tenant. project is validated the
// same way; the EMPTY project is the org's default and writes no hanzo-project tag
// (backward-compatible with every machine launched before the project dimension).
func LaunchOrgMachine(org, project string, spec *CreateMachineSpec) (*Machine, error) {
	if !validOrgSlug(org) {
		return nil, fmt.Errorf("invalid org slug %q", org)
	}
	if !validProjectSlug(project) {
		return nil, fmt.Errorf("invalid project slug %q", project)
	}
	client, err := newDigitalOceanClient()
	if err != nil {
		return nil, err
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Tags[orgTagKey] = org
	if project != "" {
		spec.Tags[projectTagKey] = project
	}
	machine, err := client.CreateMachine(spec)
	if err != nil {
		return nil, err
	}
	// A launched bot is an org member AND a playground node: register it as an
	// IAM agent-user (surfaces in hanzo.team) and plant it in the org playground
	// node registry attributed to org. Both best-effort — a registration failure
	// never fails the launch; the bot still runs and each registry reconciles
	// later (IAM on next sync, playground on the runtime heartbeat).
	if specIsBot(spec) {
		registerBotUser(org, spec.Name, spec.DisplayName)
		registerPlaygroundNode(org, spec.Name)
	}
	return machine, nil
}
