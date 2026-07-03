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

// compute.go is the canonical /v1 resell compute surface: the cached
// DigitalOcean catalog (regions/sizes/GPUs, resale-priced) and per-org machine
// operations backed by Hanzo's house DO account. Every machine endpoint is
// scoped to the caller's org, which is derived from the authenticated IAM
// identity (never trusted from a client-supplied field). Launch is metered
// through commerce (per-org debit); a dryRun returns a price quote and spends
// nothing.
package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/visor/service"
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
func (c *ApiController) resolveComputeOrg() string {
	// An authenticated principal's org (session or Bearer) is authoritative and
	// NOT overridable by a client-supplied ?owner. An authenticated user with an
	// empty Owner claim resolves to "" and the caller fails closed — it does NOT
	// fall through to ?owner. Only an unauthenticated service/app call (Basic
	// client-id/secret; no session/Bearer user) may pass ?owner, and ApiFilter
	// has already authorized it as subOwner=="app".
	if u := c.GetSessionUser(); u != nil {
		return strings.TrimSpace(u.Owner)
	}
	return strings.TrimSpace(c.Input().Get("owner"))
}

// ---- Catalog (cached, resale-priced) ----

// GetComputeRegions
// @Title GetComputeRegions
// @Tag Compute API
// @Description list DigitalOcean regions (cached)
// @Success 200 {object} controllers.Response
// @router /regions [get]
func (c *ApiController) GetComputeRegions() {
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}
	regions, err := service.ListRegions()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(regions)
}

// GetComputeSizes
// @Title GetComputeSizes
// @Tag Compute API
// @Description list resale-priced sizes (cached)
// @Success 200 {object} controllers.Response
// @router /sizes [get]
func (c *ApiController) GetComputeSizes() {
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}
	sizes, err := service.ListSizes()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(sizes)
}

// GetComputeGPUs
// @Title GetComputeGPUs
// @Tag Compute API
// @Description list resale-priced GPU sizes (cached)
// @Success 200 {object} controllers.Response
// @router /gpus [get]
func (c *ApiController) GetComputeGPUs() {
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}
	gpus, err := service.ListGPUSizes()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(gpus)
}

// ---- Machines (per-org, house account) ----

// ListComputeMachines
// @Title ListComputeMachines
// @Tag Compute API
// @Description list the caller org's machines
// @Success 200 {object} controllers.Response
// @router /machines [get]
func (c *ApiController) ListComputeMachines() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}
	machines, err := service.ListOrgMachines(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(machines)
}

// GetComputeMachine
// @Title GetComputeMachine
// @Tag Compute API
// @Description get one of the caller org's machines by id
// @router /machines/:id [get]
func (c *ApiController) GetComputeMachine() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	id := c.Ctx.Input.Param(":id")
	machine, err := service.GetOrgMachine(org, id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if machine == nil {
		c.ResponseError("machine not found")
		return
	}
	c.ResponseOk(machine)
}

// DeleteComputeMachine
// @Title DeleteComputeMachine
// @Tag Compute API
// @Description delete one of the caller org's machines by id
// @router /machines/:id [delete]
func (c *ApiController) DeleteComputeMachine() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	id := c.Ctx.Input.Param(":id")
	if err := service.DeleteOrgMachine(org, id); err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk("deleted")
}

// launchComputeRequest is the body for POST /v1/machines/launch. It embeds the
// provider spec and adds a size alias plus a dryRun flag (quote only, no spend).
type launchComputeRequest struct {
	service.CreateMachineSpec
	Size   string `json:"size"`
	Kind   string `json:"kind"`
	DryRun bool   `json:"dryRun"`
}

// LaunchQuote is the resale price quote for a size in a region (Hanzo price
// only — wholesale/provider never surfaced).
type LaunchQuote struct {
	Org          string           `json:"org"`
	Size         string           `json:"size"`
	Region       string           `json:"region"`
	Currency     string           `json:"currency"`
	PriceHourly  float64          `json:"priceHourly"`
	PriceMonthly float64          `json:"priceMonthly"`
	GPU          *service.GPUSpec `json:"gpu,omitempty"`
}

// LaunchComputeMachine
// @Title LaunchComputeMachine
// @Tag Compute API
// @Description quote (dryRun) or launch a metered, per-org machine
// @router /machines/launch [post]
func (c *ApiController) LaunchComputeMachine() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}

	var req launchComputeRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	size := strings.TrimSpace(req.Size)
	if size == "" {
		size = strings.TrimSpace(req.InstanceType)
	}
	if size == "" {
		c.ResponseError("size is required")
		return
	}

	si, err := service.SizeBySlug(size)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if si == nil {
		c.ResponseError("unknown size: " + size)
		return
	}

	quote := LaunchQuote{
		Org:          org,
		Size:         size,
		Region:       req.Region,
		Currency:     si.Currency,
		PriceHourly:  si.PriceHourly,
		PriceMonthly: si.PriceMonthly,
		GPU:          si.GPU,
	}

	// Dry run: prove resale pricing without provisioning or billing.
	if req.DryRun {
		c.ResponseOk(quote)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	meter := service.NewMeteringClient(org)
	firstHourCents := service.PriceToCents(si.PriceHourly)

	// Pre-flight balance gate — fail closed (a paid product does not launch on
	// an unknown balance) AND require the org can cover at least the first hour,
	// so a near-zero balance can't green-light an expensive GPU (AmountCents
	// gate, not the bare available>0).
	if err := meter.Authorize(ctx, metering.AuthInput{User: org, Actor: org, Org: org, Currency: "usd", AmountCents: firstHourCents}); err != nil {
		if err == metering.ErrInsufficientBalance {
			c.ResponseError("insufficient balance to launch machine")
			return
		}
		c.ResponseError("billing authorization failed: " + err.Error())
		return
	}

	spec := req.CreateMachineSpec
	spec.InstanceType = size
	// Record the requested kind (default machine - a raw single launch is
	// agent-less) so it flows onto the droplet tags and gates the agent install.
	service.SetKind(&spec, req.Kind)
	machine, err := service.LaunchOrgMachine(org, &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Roll a launched event into the analytics datastore (best-effort; never
	// blocks or fails the launch) - the analytical mirror of the commerce
	// debit below.
	service.EmitCompute(org, service.ComputeLaunched, machine, firstHourCents)

	// Debit the FIRST hour of resale price to the org at launch. Every SUBSEQUENT
	// hour a running machine stays up is debited by service.MeterRunningMachines
	// (the hourly ticker) on the SAME commerce/metering path — so a bound machine
	// keeps drawing down the org's balance. The launch owns the LAUNCH hour; the
	// recurring sweep explicitly SKIPS a machine whose create time is in the
	// current hour (service.meterMachines/createdInHour), so launch + a sweep
	// landing in the same clock hour never double-bill that hour. (Commerce does
	// not dedup on requestId, so this skip — not the requestId — is what prevents
	// the overlap; cross-replica overlap is prevented by the ticker's per-hour
	// single-flight lease.)
	if _, err := meter.Record(ctx, metering.Usage{
		User:        org,
		Actor:       org,
		Org:         org,
		Currency:    "usd",
		AmountCents: firstHourCents,
		Provider:    "compute",
		Model:       size,
		Status:      "launched",
		RequestID:   machine.Id,
	}); err != nil {
		// The machine exists; a metering write failure must not 500 the launch.
		// Surface it in the payload so the ticker/reconciler can reconcile.
		c.ResponseOk(map[string]interface{}{"machine": machine, "quote": quote, "meteringError": err.Error()})
		return
	}

	c.ResponseOk(map[string]interface{}{"machine": machine, "quote": quote})
}
