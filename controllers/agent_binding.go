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
// id. When the caller passes a bare name (no `/`), the owner is taken from the
// `owner` query param — the same fallback the verb-style endpoints use — so the
// operator can address a machine by either form.
func (c *ApiController) machineIdFromPath() string {
	id := c.Ctx.Input.Param(":id")
	if owner, _ := util.GetOwnerAndNameFromIdNoCheck(id); owner != "" {
		return id
	}
	owner := c.Input().Get("owner")
	if owner == "" {
		return id
	}
	return util.GetIdFromOwnerAndName(owner, id)
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
	owner := c.Input().Get("owner")
	if owner == "" {
		c.ResponseError("owner is required")
		return
	}

	bindings, err := object.GetAgentBindings(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(bindings)
}
