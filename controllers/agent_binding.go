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

	"github.com/hanzoai/visor/conf"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// bindAgentRequest is the `POST /v1/machines/:id/bind-agent` body.
type bindAgentRequest struct {
	Org        string `json:"org"`
	AgentName  string `json:"agentName"`
	BotVersion string `json:"botVersion"`
}

// machineIdFromPath resolves the `:id` path param into an `owner/name` machine
// id. `:id` is a single beego path segment, so it never carries a literal `/`;
// callers address a machine as EITHER a pre-joined `owner%2Fname` (rare) OR a
// bare `name` plus the `owner` query param — the same fallback the verb-style
// endpoints use. Returns "" when no owner can be resolved (the handler then
// rejects with a clear error rather than operating on a half-formed id).
//
// It must never panic on caller input: an id that is not already `owner/name`
// is combined with `?owner=`; a bare id with no `?owner=` yields "".
func (c *ApiController) machineIdFromPath() string {
	id := strings.TrimSpace(c.Ctx.Input.Param(":id"))
	if id == "" {
		return ""
	}
	// Already a well-formed `owner/name` id (e.g. url-encoded slash).
	if owner, name := splitOwnerName(id); owner != "" && name != "" {
		return util.GetIdFromOwnerAndName(owner, name)
	}
	owner := strings.TrimSpace(c.Input().Get("owner"))
	if owner == "" {
		return ""
	}
	return util.GetIdFromOwnerAndName(owner, id)
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
// routes. The global ABAC ApiFilter cannot scope these routes: it derives the
// object owner from the query `id`/body `owner`, but these routes carry the
// machine id in the PATH (`:id`) — so the filter always sees objOwner="" and
// falls back to coarse subject rules (and in demo mode lets every GET through).
// This guard closes that gap in the handler (defense in depth), mirroring
// authz.IsAllowed's subject model exactly:
//
//   - the trusted service credential (clientId/clientSecret) → allow (it is the
//     operator / `app` subject, IsAllowed's `subOwner=="app"` fast-path);
//   - a signed-in global admin (`built-in` owner or IsAdmin) → allow;
//   - a signed-in user whose org owns the machine (`user.Owner == objOwner`) →
//     allow; anyone else → deny.
//
// Returns true when the request is denied (the caller returns immediately),
// matching the RequireSignedIn convention.
func (c *ApiController) authorizeMachineOwner(machineId string) bool {
	objOwner, _ := splitOwnerName(machineId)
	if objOwner == "" {
		c.ResponseError("invalid machine id (expected owner/name)")
		return true
	}

	// Trusted service credential (the operator). Same secret the router filter
	// checks; presence of a matching clientId/clientSecret == the `app` subject.
	if c.hasServiceCredential() {
		return false
	}

	user := c.GetSessionUser()
	if user == nil {
		c.ResponseError("please sign in first")
		return true
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

// hasServiceCredential reports whether the request carries the configured
// service clientId/clientSecret (via BasicAuth or `?clientId=&clientSecret=`),
// the credential the operator uses. It reuses the exact secret comparison the
// router's ApiFilter performs so there is one source of truth for "is this the
// trusted service caller".
func (c *ApiController) hasServiceCredential() bool {
	clientId, clientSecret, ok := c.Ctx.Request.BasicAuth()
	if !ok {
		clientId = c.Input().Get("clientId")
		clientSecret = c.Input().Get("clientSecret")
	}
	if clientId == "" || clientSecret == "" {
		return false
	}
	configured := conf.GetConfigString("clientSecret")
	if configured == "" {
		return false
	}
	return clientSecret == configured
}

// BindAgent
// @Title BindAgent
// @Tag AgentBinding API
// @Description bind a cloud Agent to a machine — mark it as running the @hanzo/bot runtime
// @Param   id     path    string  true        "The id ( owner/name ) of the machine"
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
// @Param   id     path    string  true        "The id ( owner/name ) of the machine"
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
// @Param   id     path    string  true        "The id ( owner/name ) of the machine"
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
// @Description list all agent bindings owned by `owner`
// @Param   owner  query   string  true        "The owner"
// @Success 200 {array} object.AgentBinding The Response object
// @router /agent-bindings [get]
func (c *ApiController) GetAgentBindings() {
	owner := strings.TrimSpace(c.Input().Get("owner"))
	if owner == "" {
		c.ResponseError("owner is required")
		return
	}
	// Scope the list to the caller's org: reuse the machine-owner guard with a
	// synthetic `owner/*` id so a user can only list their own org's bindings
	// (the service credential and global admins may list any).
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
