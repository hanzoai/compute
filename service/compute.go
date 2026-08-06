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
// HOUSE DigitalOcean account. It is distinct from the per-owner "bring your own
// cloud" Provider path (machine_cloud.go): here ONE Hanzo DO token (from KMS)
// backs every tenant, and droplets are namespaced by an org tag so list/get/
// delete are scoped to the caller's org at the DigitalOcean layer — never the
// whole account. The catalog (regions/sizes/GPUs) is fetched once and cached
// so the dashboard is fast and DO is not hammered.
package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/godo"
	"github.com/hanzoai/visor/conf"
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

// houseDOToken resolves Hanzo's DigitalOcean API token. KMS is the only source:
// the operator wires it from KMS into DIGITALOCEAN_ACCESS_TOKEN (or the app.conf
// digitalOceanToken key, itself KMS-synced via the visor-kms-sync KMSSecret).
// Never hard-coded. Empty means "compute not configured" and every house
// endpoint fails closed.
func houseDOToken() string {
	if t := strings.TrimSpace(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(conf.GetConfigString("digitalOceanToken"))
}

// newHouseDOClient builds a DigitalOcean client bound to Hanzo's house token.
func newHouseDOClient() (MachineDigitalOceanClient, error) {
	token := houseDOToken()
	if token == "" {
		return MachineDigitalOceanClient{}, fmt.Errorf("hanzo compute is not configured: DigitalOcean token unset (resolve from KMS)")
	}
	return newMachineDigitalOceanClient("", token, "")
}

// ComputeConfigured reports whether the house DO token is present, so callers
// can return a clean 503 instead of a cryptic client error.
func ComputeConfigured() bool { return houseDOToken() != "" }

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
	client, err := newHouseDOClient()
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

// ---- Org-scoped machine operations (house account) ----

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
	client, err := newHouseDOClient()
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
// cluster in Hanzo's HOUSE DigitalOcean account tagged for org — the house
// analogue of ListOrgMachines, but for managed-Kubernetes nodes. DOKS worker
// droplets carry k8s tags, not a hanzo-org DROPLET tag, so they never surface
// through ListOrgMachines; this lists them via the managed-Kubernetes API and maps
// each node to the same Machine shape. Per-org isolation is by the cluster's
// hanzo-org tag — a tenant can only ever see its own clusters' nodes.
func ListOrgKubernetesNodes(org string) ([]*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	return kubernetesNodeMachinesByTag(context.Background(), client.Client, orgTag(org))
}

// newHouseDOKSClient builds a DOKS client on Hanzo's house token for ACCOUNT-level
// cluster operations (list/get/create/delete), which address a cluster by id rather
// than a fixed ClusterID — so the ClusterID field is left empty. It is the cluster
// analogue of newHouseDOClient; per-org isolation is enforced by the callers below
// via the hanzo-org tag, never by this client.
func newHouseDOKSClient() (*DOKSClient, error) {
	client, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	return &DOKSClient{Client: client.Client}, nil
}

// ListOrgKubernetesClusters returns every DOKS cluster in Hanzo's HOUSE account
// tagged for org — the house analogue of ListOrgMachines for whole clusters. Per-org
// isolation is by the cluster's hanzo-org tag: a tenant only ever sees its own
// clusters, never another org's.
func ListOrgKubernetesClusters(org string) ([]*KubernetesCluster, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newHouseDOKSClient()
	if err != nil {
		return nil, err
	}
	return clustersByTag(context.Background(), client.Client, orgTag(org))
}

// GetOrgKubernetesCluster returns one house cluster's detail (pools + worker nodes),
// but ONLY if it carries the caller org's hanzo-org tag. A cluster owned by another
// org — or a missing cluster — resolves to (nil, nil): the controller renders it as
// "not found", so a tenant can never read another tenant's cluster by guessing an id.
func GetOrgKubernetesCluster(org, id string) (*KubernetesClusterDetail, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newHouseDOKSClient()
	if err != nil {
		return nil, err
	}
	detail, err := client.GetCluster(context.Background(), id)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !clusterHasTag(detail.Tags, orgTag(org)) {
		return nil, nil // isolation: not this org's cluster
	}
	return detail, nil
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

// CreateOrgKubernetesCluster provisions a DOKS cluster in Hanzo's house account for
// org, stamping it managed-by + hanzo-org:<org> so it associates to the tenant
// exactly like a droplet — which is what makes it visible to that org's cluster and
// node listers (and invisible to every other org).
//
// HOUSE ACCOUNT means Hanzo pays the upstream bill for every node in the seed
// pool, so this goes through the money gate exactly like a droplet launch — and
// records the pool it provisioned, so the sweep keeps billing it.
func CreateOrgKubernetesCluster(org, project string, spec *CreateClusterSpec, record recordSeed) (*KubernetesCluster, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newHouseDOKSClient()
	if err != nil {
		return nil, err
	}
	return createClusterMetered(context.Background(), client, record, org, project, spec)
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

// DeleteOrgKubernetesCluster destroys a house cluster by id, but ONLY if it carries
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
	client, err := newHouseDOKSClient()
	if err != nil {
		return err
	}
	return deleteClusterMetered(context.Background(), client, forget, org, id)
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

// ListRunningHouseMachines returns every RUNNING droplet in Hanzo's house
// account that carries a hanzo-org tag — the set the recurring hourly meter
// debits. It lists across ALL orgs (no per-org tag filter): the org is recovered
// per machine from its own tag, so ONE sweep meters every tenant's running
// machines. Untagged/non-resell droplets (no hanzo-org tag) are excluded, so a
// non-resell house droplet is never billed to a tenant. Only "Running" machines
// are returned — a stopped droplet consumes no compute-hour.
func ListRunningHouseMachines() ([]*Machine, error) {
	client, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	orgPrefix := orgTagKey + ":"
	var machines []*Machine
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		droplets, resp, err := client.Client.Droplets.List(context.Background(), opt)
		if err != nil {
			return nil, fmt.Errorf("list house droplets: %w", err)
		}
		for _, d := range droplets {
			if d.Status != "active" { // godo "active" == running
				continue
			}
			hasOrg := false
			for _, t := range d.Tags {
				if strings.HasPrefix(t, orgPrefix) {
					hasOrg = true
					break
				}
			}
			if !hasOrg {
				continue // not a resell machine — never bill it to a tenant
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
	client, err := newHouseDOClient()
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
	client, err := newHouseDOClient()
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

// LaunchOrgMachine provisions a droplet in Hanzo's house account, tagged so it
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
	client, err := newHouseDOClient()
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
