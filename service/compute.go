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
	"github.com/hanzoai/vm/conf"
)

// orgTagKey/orgTag namespace droplets by the Hanzo org that owns them. Per-org
// isolation is enforced by querying DigitalOcean with this exact tag, so one
// tenant can never enumerate another's machines.
const orgTagKey = "hanzo-org"

func orgTag(org string) string { return orgTagKey + ":" + org }

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

// ListOrgMachines returns only the droplets tagged for org — per-org isolation
// enforced at the DigitalOcean layer via an exact tag query.
func ListOrgMachines(org string) ([]*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	var machines []*Machine
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		droplets, resp, err := client.Client.Droplets.ListByTag(context.Background(), orgTag(org), opt)
		if err != nil {
			return nil, fmt.Errorf("list droplets for org %q: %w", org, err)
		}
		for _, d := range droplets {
			machines = append(machines, getMachineFromDroplet(d))
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}
	return machines, nil
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

func dropletHasOrgTag(d *godo.Droplet, org string) bool {
	want := orgTag(org)
	for _, t := range d.Tags {
		if t == want {
			return true
		}
	}
	return false
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
	return nil
}

// LaunchOrgMachine provisions a droplet in Hanzo's house account, tagged so it
// is owned by org. The org tag is injected here (never trusted from the client
// body) so the machine is always attributable to the right tenant.
func LaunchOrgMachine(org string, spec *CreateMachineSpec) (*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	client, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Tags[orgTagKey] = org
	return client.CreateMachine(spec)
}
