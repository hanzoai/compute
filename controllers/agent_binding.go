// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package controllers

import (
	"encoding/json"
	"strings"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// bindAgentRequest is the `POST /v1/machines/:id/bind-agent` body.
type bindAgentRequest struct {
	Org        string `json:"org"`
	AgentName  string `json:"agentName"`
	BotVersion string `json:"botVersion"`
}

// machineIdFromPath resolves the `:id` path param into a fully-qualified
// `owner/name` machine id, IAM-native and spoof-proof: the OWNER is always the
// caller's resolved org (resolveComputeOrg — an authenticated user's Owner claim,
// never a client-supplied field), and the NAME is the `:id` path segment. A user
// therefore cannot address another org's machine by crafting the path; the id it
// can build is always scoped to its own org. The service/app subject (Basic
// client secret, already authorized upstream by ApiFilter) resolves its org from
// `?owner=`, matching the rest of the resell compute surface.
//
// It never panics on caller input. Returns "" when no org context can be
// resolved (empty Owner / no ?owner) OR the `:id` segment is empty — the handler
// then rejects with a clear error rather than operating on a half-formed id.
func (c *ApiController) machineIdFromPath() string {
	name := strings.TrimSpace(c.Ctx.Input.Param(":id"))
	if name == "" {
		return ""
	}
	org := c.resolveComputeOrg()
	if org == "" {
		return ""
	}
	return util.GetIdFromOwnerAndName(org, name)
}

// splitOwnerName splits an `owner/name` id without panicking on malformed input
// (unlike util.GetOwnerAndNameFromId which panics, and the NoCheck variant which
// index-panics on a single token). Returns ("","") unless the id is exactly two
// non-empty slash-separated tokens.
func splitOwnerName(id string) (string, string) {
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 || tokens[0] == "" || tokens[1] == "" {
		return "", ""
	}
	return tokens[0], tokens[1]
}

// authorizeMachineOwner is the object-level tenant guard for the agent-binding
// routes (defense in depth, applied by every handler). The global ABAC ApiFilter
// admits these routes for any authenticated brand user via
// authz.isResellComputePath, deferring org-scoping to the controller — exactly
// the model the rest of the resell compute surface uses. This guard closes that
// gap in the handler, mirroring authz.IsAllowed's subject model exactly:
//
//   - the trusted service/app subject (Basic clientId/clientSecret, already
//     authenticated by ApiFilter as subOwner=="app"; GetSessionUser()==nil) →
//     allow — it is the operator, and its org is the ?owner it supplied;
//   - a signed-in global admin (`built-in` owner or IsAdmin) → allow;
//   - a signed-in user whose org owns the machine (user.Owner == objOwner) →
//     allow (structurally guaranteed, since machineIdFromPath builds objOwner
//     FROM resolveComputeOrg == user.Owner — the mismatch branch is belt-and-
//     suspenders); anyone else / forbidden / deleted → deny.
//
// Returns true when the request is denied (the caller returns immediately),
// matching the RequireSignedIn convention.
func (c *ApiController) authorizeMachineOwner(machineId string) bool {
	objOwner, _ := splitOwnerName(machineId)
	if objOwner == "" {
		c.ResponseError("invalid machine id (expected owner/name)")
		return true
	}

	user := c.GetSessionUser()
	if user == nil {
		// Service/app subject: ApiFilter has already authenticated it as
		// subOwner=="app" (the only way an unauthenticated caller reaches this
		// handler), and its org == the ?owner it supplied via resolveComputeOrg.
		return false
	}
	if user.IsForbidden || user.IsDeleted {
		c.ResponseError("Unauthorized operation")
		return true
	}
	// Global admin: the `built-in` org or an explicit admin flag.
	if user.Owner == "built-in" || user.IsAdmin {
		return false
	}
	if user.Owner == objOwner {
		return false
	}

	c.ResponseError("Unauthorized operation: machine belongs to a different owner")
	return true
}

// BindAgent
// @Title BindAgent
// @Tag AgentBinding API
// @Description bind a cloud Agent to a machine — mark it as running the @hanzo/bot runtime
// @Param   id     path    string  true        "The name of the machine (scoped to the caller org)"
// @Param   body   body    controllers.bindAgentRequest  true  "org + agentName (+ optional botVersion)"
// @Success 200 {object} object.AgentBinding The Response object
// @router /machines/:id/bind-agent [post]
func (c *ApiController) BindAgent() {
	machineId := c.machineIdFromPath()
	if machineId == "" {
		c.ResponseError("machine id is required")
		return
	}
	if c.authorizeMachineOwner(machineId) {
		return
	}

	var req bindAgentRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if req.Org == "" || req.AgentName == "" {
		c.ResponseError("org and agentName are required")
		return
	}

	binding, err := object.BindAgent(machineId, req.Org, req.AgentName, req.BotVersion)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(binding)
}

// GetAgentBinding
// @Title GetAgentBinding
// @Tag AgentBinding API
// @Description get a machine's agent binding (status re-reconciled against live machine state)
// @Param   id     path    string  true        "The name of the machine (scoped to the caller org)"
// @Success 200 {object} object.AgentBinding The Response object
// @router /machines/:id/agent-binding [get]
func (c *ApiController) GetAgentBinding() {
	machineId := c.machineIdFromPath()
	if machineId == "" {
		c.ResponseError("machine id is required")
		return
	}
	if c.authorizeMachineOwner(machineId) {
		return
	}

	binding, err := object.ReconcileAgentBinding(machineId)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(binding)
}

// UnbindAgent
// @Title UnbindAgent
// @Tag AgentBinding API
// @Description remove a machine's agent binding (does not delete the machine)
// @Param   id     path    string  true        "The name of the machine (scoped to the caller org)"
// @Success 200 {object} controllers.Response The Response object
// @router /machines/:id/agent-binding [delete]
func (c *ApiController) UnbindAgent() {
	machineId := c.machineIdFromPath()
	if machineId == "" {
		c.ResponseError("machine id is required")
		return
	}
	if c.authorizeMachineOwner(machineId) {
		return
	}

	c.Data["json"] = wrapActionResponse(object.UnbindAgent(machineId))
	c.ServeJSON()
}

// GetAgentBindings
// @Title GetAgentBindings
// @Tag AgentBinding API
// @Description list all agent bindings owned by the caller org
// @Success 200 {array} object.AgentBinding The Response object
// @router /agent-bindings [get]
func (c *ApiController) GetAgentBindings() {
	// The org is the caller's resolved org (spoof-proof) — a client-supplied
	// ?owner is honored only for the service/app subject, exactly as
	// resolveComputeOrg governs the rest of the resell compute surface. A user
	// can therefore only ever list its OWN org's bindings.
	owner := c.resolveComputeOrg()
	if owner == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	// Defense in depth: reuse the machine-owner guard with a synthetic `owner/*`
	// id (the service credential and global admins may list any).
	if c.authorizeMachineOwner(util.GetIdFromOwnerAndName(owner, "*")) {
		return
	}

	bindings, err := object.GetAgentBindings(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(bindings)
}
