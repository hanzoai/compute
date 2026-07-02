// fleet.go — launch & manage a NAMED FLEET of bot-machines over the same /v1
// resell-compute surface. A fleet is N machines named "<fleet>-NNN"; each member
// is a bot: LaunchOrgMachine provisions the droplet and cloud-init installs
// @hanzo/bot as a systemd service that joins the gateway at wss://gw.hanzo.bot.
//
// Fleet ops are batched single-machine ops — the SAME provisioner, the SAME
// per-machine commerce metering (authorize -> launch -> debit the launch hour;
// the hourly ticker debits every subsequent hour) — so there is exactly ONE way
// a machine is launched, billed and destroyed, whether alone or as a member.
package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/visor/service"
)

const fleetSep = "-"

// fleetMemberName is the canonical name of member i: "<fleet>-NNN".
func fleetMemberName(fleet string, i int) string {
	return fmt.Sprintf("%s%s%03d", fleet, fleetSep, i)
}

// fleetOf maps a member name "<fleet>-NNN" back to "<fleet>", or "" if the name
// is not a fleet member (no 3-digit numeric suffix).
func fleetOf(name string) string {
	i := strings.LastIndex(name, fleetSep)
	if i <= 0 || len(name)-i-1 != 3 {
		return ""
	}
	for _, r := range name[i+1:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return name[:i]
}

type fleetLaunchRequest struct {
	service.CreateMachineSpec        // region, os, imageId, tags (env:BOT_* passthrough), sshKeyIds
	Name      string `json:"name"`  // fleet name; members are <name>-000, <name>-001, ...
	Count     int    `json:"count"` // desired number of bots
	Size      string `json:"size"`  // size slug for every member
	DryRun    bool   `json:"dryRun"`
}

type FleetMember struct {
	Name     string `json:"name"`
	Id       string `json:"id"`
	State    string `json:"state"`
	PublicIp string `json:"publicIp,omitempty"`
}

func memberOf(m *service.Machine) FleetMember {
	return FleetMember{Name: m.Name, Id: m.Id, State: m.State, PublicIp: m.PublicIp}
}

type Fleet struct {
	Name        string        `json:"name"`
	Count       int           `json:"count"`
	Size        string        `json:"size,omitempty"`
	Currency    string        `json:"currency,omitempty"`
	PriceHourly float64       `json:"priceHourly"` // per bot
	FleetHourly float64       `json:"fleetHourly"` // Count * PriceHourly
	Members     []FleetMember `json:"members"`
}

// launchMetered is the ONE canonical metered launch, shared by single-machine
// and fleet launch: authorize the org for the first hour of si, provision +
// bot-bootstrap via LaunchOrgMachine, debit the launch hour. Fail-closed — an
// unknown or insufficient balance launches nothing and spends nothing.
func launchMetered(ctx context.Context, org string, spec *service.CreateMachineSpec, si *service.SizeInfo) (*service.Machine, error) {
	meter := service.NewMeteringClient(org)
	firstHourCents := service.PriceToCents(si.PriceHourly)
	if err := meter.Authorize(ctx, metering.AuthInput{User: org, Actor: org, Org: org, Currency: "usd", AmountCents: firstHourCents}); err != nil {
		if err == metering.ErrInsufficientBalance {
			return nil, fmt.Errorf("insufficient balance to launch machine")
		}
		return nil, fmt.Errorf("billing authorization failed: %v", err)
	}
	machine, err := service.LaunchOrgMachine(org, spec)
	if err != nil {
		return nil, err
	}
	// Machine exists; a metering write failure must not fail the launch (the
	// ticker/reconciler reconciles). Same debit contract as single-machine launch.
	_, _ = meter.Record(ctx, metering.Usage{
		User: org, Actor: org, Org: org, Currency: "usd", AmountCents: firstHourCents,
		Provider: "compute", Model: spec.InstanceType, Status: "launched", RequestID: machine.Id,
	})
	return machine, nil
}

// members returns the fleet's current member machines, sorted by name (== index).
func fleetMembers(org, fleet string) ([]*service.Machine, error) {
	all, err := service.ListOrgMachines(org)
	if err != nil {
		return nil, err
	}
	var out []*service.Machine
	for _, m := range all {
		if fleetOf(m.Name) == fleet {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ensureFleet brings the named fleet to `want` members: launches missing
// low-index members up to `want`, reusing existing ones by name (idempotent).
// Returns the resulting members. Fail-closed per member; a partial fleet plus
// the error is surfaced by the caller.
func ensureFleet(ctx context.Context, org, name string, want int, base service.CreateMachineSpec, size string, si *service.SizeInfo) ([]FleetMember, error) {
	existing := map[string]*service.Machine{}
	cur, err := fleetMembers(org, name)
	if err != nil {
		return nil, err
	}
	for _, m := range cur {
		existing[m.Name] = m
	}
	var members []FleetMember
	for i := 0; i < want; i++ {
		mName := fleetMemberName(name, i)
		if m, ok := existing[mName]; ok {
			members = append(members, memberOf(m))
			continue
		}
		spec := base
		spec.Name = mName
		if spec.DisplayName == "" {
			spec.DisplayName = mName
		}
		spec.InstanceType = size
		m, err := launchMetered(ctx, org, &spec, si)
		if err != nil {
			return members, err
		}
		members = append(members, memberOf(m))
	}
	return members, nil
}

// LaunchFleet
// @Title LaunchFleet
// @Tag Compute API
// @Description quote (dryRun) or launch/ensure a named fleet of N bot-machines
// @router /fleet [post]
func (c *ApiController) LaunchFleet() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	var req fleetLaunchRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || fleetOf(name) != "" {
		c.ResponseError("fleet name is required and must not look like a member (<name>-NNN)")
		return
	}
	if req.Count < 1 {
		c.ResponseError("count must be >= 1")
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

	fleet := Fleet{
		Name: name, Count: req.Count, Size: size, Currency: si.Currency,
		PriceHourly: si.PriceHourly, FleetHourly: si.PriceHourly * float64(req.Count),
	}
	if req.DryRun {
		c.ResponseOk(fleet)
		return
	}
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(60*req.Count)*time.Second)
	defer cancel()

	members, err := ensureFleet(ctx, org, name, req.Count, req.CreateMachineSpec, size, si)
	fleet.Members = members
	fleet.Count = len(members)
	if err != nil {
		c.ResponseOk(map[string]any{"fleet": fleet, "error": err.Error()})
		return
	}
	c.ResponseOk(fleet)
}

// ListFleets
// @Title ListFleets
// @Tag Compute API
// @Description list the caller org's fleets (machines grouped by name prefix)
// @router /fleets [get]
func (c *ApiController) ListFleets() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	all, err := service.ListOrgMachines(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	byName := map[string]*Fleet{}
	var order []string
	for _, m := range all {
		f := fleetOf(m.Name)
		if f == "" {
			continue
		}
		fl, ok := byName[f]
		if !ok {
			fl = &Fleet{Name: f}
			byName[f] = fl
			order = append(order, f)
		}
		fl.Members = append(fl.Members, memberOf(m))
		fl.Count = len(fl.Members)
	}
	sort.Strings(order)
	out := make([]*Fleet, 0, len(order))
	for _, f := range order {
		sort.Slice(byName[f].Members, func(i, j int) bool { return byName[f].Members[i].Name < byName[f].Members[j].Name })
		out = append(out, byName[f])
	}
	c.ResponseOk(out)
}

// GetFleet
// @Title GetFleet
// @Tag Compute API
// @Description get one fleet's members
// @router /fleet/:name [get]
func (c *ApiController) GetFleet() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	name := strings.TrimSpace(c.Ctx.Input.Param(":name"))
	ms, err := fleetMembers(org, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	fleet := Fleet{Name: name, Count: len(ms)}
	for _, m := range ms {
		fleet.Members = append(fleet.Members, memberOf(m))
	}
	c.ResponseOk(fleet)
}

// ScaleFleet
// @Title ScaleFleet
// @Tag Compute API
// @Description scale a fleet to a target size (launch or destroy members)
// @router /fleet/:name/scale [post]
func (c *ApiController) ScaleFleet() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	name := strings.TrimSpace(c.Ctx.Input.Param(":name"))
	var req struct {
		Count int    `json:"count"`
		Size  string `json:"size"`
	}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if req.Count < 0 {
		c.ResponseError("count must be >= 0")
		return
	}
	cur, err := fleetMembers(org, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Scale down: destroy the highest-index members beyond count.
	if req.Count < len(cur) {
		for _, m := range cur[req.Count:] {
			if err := service.DeleteOrgMachine(org, m.Id); err != nil {
				c.ResponseError(err.Error())
				return
			}
		}
		c.GetFleet()
		return
	}

	// Scale up: derive the size from an existing member if not given, then ensure.
	size := strings.TrimSpace(req.Size)
	if size == "" && len(cur) > 0 {
		size = cur[0].Size
	}
	if size == "" {
		c.ResponseError("size is required to scale up an empty fleet")
		return
	}
	si, err := service.SizeBySlug(size)
	if err != nil || si == nil {
		c.ResponseError("unknown size: " + size)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(60*req.Count)*time.Second)
	defer cancel()
	members, err := ensureFleet(ctx, org, name, req.Count, service.CreateMachineSpec{}, size, si)
	if err != nil {
		c.ResponseOk(map[string]any{"fleet": Fleet{Name: name, Count: len(members), Members: members}, "error": err.Error()})
		return
	}
	c.ResponseOk(Fleet{Name: name, Count: len(members), Members: members})
}

// DeleteFleet
// @Title DeleteFleet
// @Tag Compute API
// @Description destroy every machine in a fleet
// @router /fleet/:name [delete]
func (c *ApiController) DeleteFleet() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	name := strings.TrimSpace(c.Ctx.Input.Param(":name"))
	ms, err := fleetMembers(org, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	deleted := 0
	for _, m := range ms {
		if err := service.DeleteOrgMachine(org, m.Id); err != nil {
			c.ResponseOk(map[string]any{"deleted": deleted, "error": err.Error()})
			return
		}
		deleted++
	}
	c.ResponseOk(map[string]any{"fleet": name, "deleted": deleted})
}
