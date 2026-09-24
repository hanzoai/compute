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
	"strings"
	"time"

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
	at := time.Now()
	err = service.Provision(ctx, org, project, firstHourCents, firstHourCents, spec.InstanceType, func() (string, error) {
		m, err := service.LaunchOrgMachine(ctx, org, project, spec)
		if err != nil {
			return "", err
		}
		machine = m
		// Roll a launched event into the analytics datastore (best-effort; never
		// blocks or fails the launch) — the analytical mirror of the commerce debit.
		service.EmitCompute(org, service.ComputeLaunched, m, firstHourCents)
		return service.LaunchCharge(m.Id, at), nil
	}, func() { service.MarkBilled(machine.Id, at) })
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
