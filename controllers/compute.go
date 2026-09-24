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

// compute.go is the canonical /v1 hosted compute surface: the catalog
// (regions/sizes/GPUs, at Hanzo's price) and per-org machines in Hanzo's own EC2
// account. Every machine endpoint is scoped to the caller's org, which is derived
// from the authenticated IAM identity (never trusted from a client-supplied
// field). Beneath org, an OPTIONAL app > project scope (from the gateway-threaded
// tenant context, or the launch body as a fallback) sharpens the analytics rollup
// without ever gating a launch.
// Launch is metered through commerce (per-org debit); a dryRun returns a price
// quote and spends nothing.
//
// A "fleet" is not a separate entity — it is just N machines launched in one
// batch (count>1) named "<name>-000", "<name>-001", …, sharing a name prefix.
// There is exactly ONE way a machine is launched, billed and destroyed
// (launchMetered), whether alone or as one of a batch.
package controllers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
)

// resolveComputeOrg returns the org that owns this request, IAM-native and
// spoof-proof:
//  1. an authenticated user's org (Owner claim) is authoritative and a
//     client-supplied owner is ignored for real users; or
//  2. for a trusted service call (authenticated as the visor app via ApiFilter,
//     e.g. the console proxy), the explicit owner query param is honored — the
//     same model the PaaS proxy uses (PAAS_ORG_ID). Only a caller holding the
//     KMS-held visor client secret reaches this branch.
//
// Empty result means "no org context" and the caller fails closed.
//
// The rule itself lives in principal (agent_binding.go) and is read from there,
// not restated here: a typed op declares the Bearer and the ?owner as INPUTS and
// so cannot reach a request to ask, while these untyped handlers fetch both off
// the Ctx. Two ways to obtain the same two strings is fine; two answers to
// "whose org is this" would not be.
func (c *ApiController) resolveComputeOrg() string {
	// The address first, for the same reason everything else reads it first: a
	// resource names its owner in the path, and the authorization seam — which
	// runs as middleware and cannot see route parameters — reads that same
	// segment. Owner in the query and owner in the path must not be able to
	// disagree, so there is one place that decides which is which.
	owner := c.Ctx.Param("owner")
	if owner == "" {
		owner = c.Ctx.Query("owner")
	}
	_, org := principal(c.Ctx.Header("Authorization"), owner)
	return org
}

// resolveComputeApp / resolveComputeProject return the OPTIONAL app / project
// scope beneath the owning org, resolved the same ONE way: the gateway-threaded
// tenant context (X-App-ID / X-Project-ID, populated by routers.TenantContextFilter
// and read back through object's getters) is authoritative; a direct API caller
// that sends no header may pass the value in the launch body as a fallback. Empty
// means "no such scope" — unlike org, app and project are optional finer
// attribution and NEVER gate a launch. The billing key stays org; app/project only
// sharpen the analytics rollup (org > app > project).
func (c *ApiController) resolveComputeApp(fallback string) string {
	if a := object.GetTenantAppID(c.Ctx); a != "" {
		return a
	}
	return strings.TrimSpace(fallback)
}

func (c *ApiController) resolveComputeProject(fallback string) string {
	if p := object.GetTenantProjectID(c.Ctx); p != "" {
		return p
	}
	return strings.TrimSpace(fallback)
}

// ---- Catalog (at Hanzo's price) ----

// GetComputeRegions
// @Title GetComputeRegions
// @Tag Compute API
// @Description list the regions hosted machines launch in
// @Success 200 {object} controllers.Response
// @router /regions [get]
func (c *ApiController) GetComputeRegions() {
	if !service.ComputeConfigured() {
		c.ResponseError(refuseNoCompute)
		return
	}
	c.ResponseOk(service.ListRegions())
}

// GetComputeSizes
// @Title GetComputeSizes
// @Tag Compute API
// @Description list the sizes for sale, at Hanzo's price
// @Success 200 {object} controllers.Response
// @router /sizes [get]
func (c *ApiController) GetComputeSizes() {
	if !service.ComputeConfigured() {
		c.ResponseError(refuseNoCompute)
		return
	}
	c.ResponseOk(service.ListSizes())
}

// GetComputeGPUs
// @Title GetComputeGPUs
// @Tag Compute API
// @Description list the GPU sizes for sale, at Hanzo's price
// @Success 200 {object} controllers.Response
// @router /gpus [get]
func (c *ApiController) GetComputeGPUs() {
	if !service.ComputeConfigured() {
		c.ResponseError(refuseNoCompute)
		return
	}
	c.ResponseOk(service.ListGPUSizes())
}

// unionMachines merges machine lists from independent sources into ONE deduped
// slice: a machine is claimed by provider id OR name, and the FIRST source to
// carry it wins (later sources contribute only what is not already present). This
// is the visor-side analogue of the cloud fleet dedup — a worker node listed by
// two sources appears once (deduped by its id). Ordered sources let the caller
// pick the winner.
func unionMachines(sources ...[]*service.Machine) []*service.Machine {
	out := []*service.Machine{}
	seen := map[string]struct{}{}
	claimed := func(m *service.Machine) bool {
		if m.Id != "" {
			if _, ok := seen["id:"+m.Id]; ok {
				return true
			}
		}
		if m.Name != "" {
			if _, ok := seen["name:"+m.Name]; ok {
				return true
			}
		}
		return false
	}
	claim := func(m *service.Machine) {
		if m.Id != "" {
			seen["id:"+m.Id] = struct{}{}
		}
		if m.Name != "" {
			seen["name:"+m.Name] = struct{}{}
		}
	}
	for _, src := range sources {
		for _, m := range src {
			if claimed(m) {
				continue
			}
			out = append(out, m)
			claim(m)
		}
	}
	return out
}

// Nodes is an org's managed-Kubernetes worker inventory, one row per node.
//
// The field is ALWAYS an array on the wire, never null and never absent: a reader
// tells "this service does not serve this op" from "this org has no nodes" by
// whether the field arrived at all, and that distinction is the only thing between
// a version skew and a fleet that silently reads as empty.
type Nodes struct {
	Nodes []*service.Machine `json:"nodes"`
}

// ListNodes lists the caller org's Kubernetes worker nodes as machines: the
// deduped union of the platform accounts (clusters carrying the hanzo-org tag)
// and BYOC providers (clusters named by Provider.ClusterID). A worker node
// appears on no other list — this op is how a cluster's workers are visible as
// machines at all.
//
// The two sources are independent, and the platform one needs a platform
// account: a deployment with none skips it rather than failing the whole read
// and hiding the BYOC nodes behind an error.
func ListNodes(_ context.Context, in *Scope) (*Nodes, error) {
	_, org := principal(in.Authorization, in.Owner)
	if org == "" {
		return nil, zip.ErrForbidden("no org context")
	}
	var platform []*service.Machine
	if service.KubernetesConfigured() {
		var err error
		platform, err = service.ListOrgKubernetesNodes(org)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
		}
	}
	byoc, err := object.GetKubernetesNodesCloud(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	return &Nodes{Nodes: unionMachines(platform, byoc)}, nil
}

// ---- k8s clusters (platform-account lifecycle, org-scoped) ----
//
// The unified /v1/k8s noun: list clusters, one cluster's detail (pools + worker
// nodes) and DEPLOY (create) / delete clusters on the platform accounts. Every
// handler is org-scoped by resolveComputeOrg (fail-closed on no org context) —
// the SAME tenant model the rest of the resell compute surface uses. Per-org
// isolation lives in the service layer (the hanzo-org cluster tag): a tenant can
// only ever see or mutate its OWN clusters.

// ListComputeKubernetesClusters
// @Title ListComputeKubernetesClusters
// @Tag Compute API
// @Description list the caller org's clusters on the platform accounts (hanzo-org tag)
// @Success 200 {object} controllers.Response
// @router /k8s/clusters [get]
func (c *ApiController) ListComputeKubernetesClusters() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	if !service.KubernetesConfigured() {
		c.ResponseError(refuseNoProvider)
		return
	}
	clusters, err := service.ListOrgKubernetesClusters(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(clusters)
}

// GetComputeKubernetesCluster
// @Title GetComputeKubernetesCluster
// @Tag Compute API
// @Description get one of the caller org's clusters by id — detail incl. node pools and worker nodes
// @router /k8s/clusters/:id [get]
func (c *ApiController) GetComputeKubernetesCluster() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	if !service.KubernetesConfigured() {
		c.ResponseError(refuseNoProvider)
		return
	}
	id := c.Ctx.Param("id")
	detail, err := service.GetOrgKubernetesCluster(org, id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if detail == nil {
		c.ResponseError("cluster not found")
		return
	}
	c.ResponseOk(detail)
}

// CreateComputeKubernetesCluster
// @Title CreateComputeKubernetesCluster
// @Tag Compute API
// @Description provision a cluster for the caller org (body: name, region, version, nodePool{size,count})
// @router /k8s/clusters [post]
func (c *ApiController) CreateComputeKubernetesCluster() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	if !service.KubernetesConfigured() {
		c.ResponseError(refuseNoProvider)
		return
	}
	// The request body IS the service spec (one shape, no re-mapping).
	var spec service.CreateClusterSpec
	if err := json.Unmarshal(c.Ctx.Body(), &spec); err != nil {
		c.ResponseError(err.Error())
		return
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.NodePool.Size = strings.TrimSpace(spec.NodePool.Size)
	if spec.Name == "" {
		c.ResponseError("name is required")
		return
	}
	if spec.Region == "" {
		c.ResponseError("region is required")
		return
	}
	if spec.NodePool.Size == "" {
		c.ResponseError("nodePool.size is required")
		return
	}
	if spec.NodePool.Count < 1 {
		c.ResponseError("nodePool.count must be at least 1")
		return
	}
	cluster, err := service.CreateOrgKubernetesCluster(org, c.resolveComputeProject(""), &spec, object.RecordSeedPool)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(cluster)
}

// DeleteComputeKubernetesCluster
// @Title DeleteComputeKubernetesCluster
// @Tag Compute API
// @Description delete one of the caller org's clusters by id
// @router /k8s/clusters/:id [delete]
func (c *ApiController) DeleteComputeKubernetesCluster() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	if !service.KubernetesConfigured() {
		c.ResponseError(refuseNoProvider)
		return
	}
	id := c.Ctx.Param("id")
	if err := service.DeleteOrgKubernetesCluster(org, id, object.ForgetClusterPools); err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk("deleted")
}

// launchComputeRequest is the body for POST /v1/machines. It embeds the
// provider spec and adds a size alias, a kind, an optional app/project scope, a
// dryRun flag (quote only, no spend) and a batch launch: count>1 launches N
// machines named "<name>-000", "<name>-001", … (a "fleet" is just this batch,
// sharing the name prefix). App/Project are a body-level FALLBACK for a
// direct API caller — the gateway-threaded X-App-ID / X-Project-ID header wins
// when present (resolveComputeApp / resolveComputeProject).
type launchComputeRequest struct {
	service.CreateMachineSpec
	Size    string `json:"size"`
	Kind    string `json:"kind"`
	App     string `json:"app"`     // optional org>app>project scope; header X-App-ID wins
	Project string `json:"project"` // optional; header X-Project-ID wins
	Count   int    `json:"count"`   // >1 launches a batch; 0/1 is a single machine
	Name    string `json:"name"`    // machine name; batch members are <name>-NNN
	DryRun  bool   `json:"dryRun"`
}

// LaunchQuote is the price of a size in a region: Hanzo's price only, never what
// the size costs Hanzo. CentsHourly is exactly what the launch debits for its
// first hour and the meter for every hour after.
type LaunchQuote struct {
	Org          string           `json:"org"`
	Size         string           `json:"size"`
	Region       string           `json:"region"`
	Currency     string           `json:"currency"`
	CentsHourly  int64            `json:"centsHourly"`
	PriceHourly  float64          `json:"priceHourly"`
	PriceMonthly float64          `json:"priceMonthly"`
	GPU          *service.GPUSpec `json:"gpu,omitempty"`
}

// maxBatch bounds one batch launch. A batch runs member by member under the
// org's provisioning hold, so its size is how long that hold is held.
const maxBatch = 32

// batchMemberName is the canonical name of batch member i: "<name>-NNN". A batch
// launched with count N is just N machines sharing this name prefix — there is
// no separate fleet entity.
func batchMemberName(name string, i int) string {
	return fmt.Sprintf("%s-%03d", name, i)
}

// mintMachineName names a machine whose caller did not name it.
//
// Tabs opens a scratch terminal and has no name to give, and a machine with no
// name is a blank row in every list. Naming a throwaway is not the caller's job,
// so the server does it. The kind says what it is, and four random bytes keep
// two clicks in the same second apart. Lowercased, and anything a hostname will
// not carry becomes a dash.
func mintMachineName(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		k = "machine"
	}
	k = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, k)
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Never fail a launch over a name. The clock is unique enough for the
		// only case this can happen in, which is the entropy pool being unusable.
		return fmt.Sprintf("%s-%d", k, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%x", k, b)
}

// launchMetered is the ONE metered launch shared by the single and batch launch
// paths: authorize the org for the first hour of the size, provision + bootstrap
// via LaunchOrgMachine, then debit that launch hour. Fail-closed — an unknown or
// insufficient balance launches nothing and spends nothing. The caller sets the
// spec's kind (service.SetKind) before calling; launchMetered is kind-agnostic.
// Every SUBSEQUENT running hour is debited by service.MeterRunningMachines (the
// hourly ticker) on this SAME commerce path; the launch debits its hour under
// that hour's meter id and records it in the ledger, so neither the sweep nor a
// start in the same hour charges it again.
//
// It composes the same three primitives every other provision path uses
// (service.HourlyCents → AuthorizeCompute → RecordCompute), so a machine, a node
// pool and a cluster are priced by one rule and gated by one rule.
//
// A metering WRITE failure does not fail the launch — the machine exists, and
// refusing to return it would leave the customer paying for something they were
// told they did not get. It is logged loudly instead: nothing reconciles it.
func launchMetered(ctx context.Context, org, project string, spec *service.CreateMachineSpec, si *service.SizeInfo) (*service.Machine, error) {
	firstHourCents, err := service.RateOf(si)
	if err != nil {
		return nil, err
	}
	var machine *service.Machine
	// A machine has one fixed size and does not grow on its own, so the ceiling
	// the org is authorized for and the hour it is charged are the same number.
	err = service.Provision(ctx, org, project, firstHourCents, firstHourCents, spec.InstanceType, func() (string, error) {
		m, err := service.LaunchOrgMachine(ctx, org, project, spec)
		if err != nil {
			return "", err
		}
		machine = m
		// Roll a launched event into the analytics datastore (best-effort; never
		// blocks or fails the launch) — the analytical mirror of the commerce debit.
		service.EmitCompute(org, service.ComputeLaunched, m, firstHourCents)
		return service.LaunchCharge(m.Id, time.Now()), nil
	})
	if err != nil {
		return nil, err
	}
	return machine, nil
}

// LaunchComputeMachine
// @Title LaunchComputeMachine
// @Tag Compute API
// @Description quote (dryRun) or launch a metered, per-org machine; count>1 launches a batch of <name>-NNN
// @router /machines [post]
func (c *ApiController) LaunchComputeMachine() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError(refuseNoOrg)
		return
	}

	var req launchComputeRequest
	if err := json.Unmarshal(c.Ctx.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	size := strings.TrimSpace(req.Size)
	if size == "" {
		size = strings.TrimSpace(req.InstanceType)
	}
	if size == "" {
		// A launcher with no size picker — the tabs "New cloud machine" button, a
		// bare CLI launch — still gets a machine, of the catalog's default size, so
		// the quote and the machine name one size.
		size = service.DefaultLaunchSize
	}

	si := service.SizeBySlug(size)
	if si == nil {
		c.ResponseError("unknown size: " + size)
		return
	}

	region := strings.TrimSpace(req.Region)
	quote := LaunchQuote{
		Org:          org,
		Size:         size,
		Region:       region,
		Currency:     si.Currency,
		CentsHourly:  si.CentsHourly,
		PriceHourly:  si.PriceHourly,
		PriceMonthly: si.PriceMonthly,
		GPU:          si.GPU,
	}
	if region == "" && len(si.Regions) > 0 {
		quote.Region = si.Regions[0]
	}

	// Dry run: the price, without provisioning or billing anything.
	if req.DryRun {
		c.ResponseOk(quote)
		return
	}
	if req.Count > maxBatch {
		c.ResponseError(fmt.Sprintf("a batch launches at most %d machines", maxBatch))
		return
	}

	// The batch launch budgets ~60s per member; a single launch keeps 30s.
	timeout := 30 * time.Second
	if req.Count > 1 {
		timeout = time.Duration(60*req.Count) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// One base spec — size, region, kind and the optional app>project scope set
	// once, shared by the single and every batch member. SetKind default is
	// machine (kind=bot bootstraps the @hanzo/bot agent); SetScope records the
	// resolved app/project so the machine self-describes its org>app>project
	// scope, exactly as org is set in LaunchOrgMachine. Both launch surfaces
	// (single + batch) flow through this SAME base, so scope is set exactly one way.
	base := req.CreateMachineSpec
	base.InstanceType = size
	base.Region = region
	service.SetKind(&base, req.Kind)
	project := c.resolveComputeProject(req.Project)
	service.SetScope(&base, c.resolveComputeApp(req.App), project)
	name := strings.TrimSpace(req.Name)

	// Everything a launch can be refused for without asking anyone — a size not
	// for sale here, a region not offered, an account setting missing, an image
	// or key the account cannot honour — is refused here, before the balance is
	// read or the cloud is called.
	if err := service.LaunchReady(&base); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Batch: count>1 launches N members named "<name>-NNN" through the SAME
	// metered primitive. A per-member failure returns what launched plus the error.
	if req.Count > 1 {
		if name == "" {
			c.ResponseError("name is required to launch a batch")
			return
		}
		machines := make([]*service.Machine, 0, req.Count)
		for i := 0; i < req.Count; i++ {
			spec := base
			spec.Name = batchMemberName(name, i)
			spec.DisplayName = spec.Name
			machine, err := launchMetered(ctx, org, project, &spec, si)
			if err != nil {
				c.ResponseOk(map[string]any{"machines": machines, "quote": quote, "error": err.Error()})
				return
			}
			machines = append(machines, machine)
		}
		c.ResponseOk(map[string]any{"machines": machines, "quote": quote})
		return
	}

	// Single: count<=1 keeps today's shape. The outer Name shadows the embedded
	// spec's Name in JSON, so set it explicitly.
	spec := base
	if name == "" {
		name = mintMachineName(req.Kind)
	}
	spec.Name = name
	machine, err := launchMetered(ctx, org, project, &spec, si)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(map[string]any{"machine": machine, "quote": quote})
}

// ListComputeKubernetesProviders
// @Title ListComputeKubernetesProviders
// @Tag Compute API
// @Description every configured kubernetes cloud and whether it answers right now
// @router /k8s/providers [get]
func (c *ApiController) ListComputeKubernetesProviders() {
	if c.resolveComputeOrg() == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	c.ResponseOk(service.KubernetesProviderStatus(context.Background()))
}
