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
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

// GetMachines
// @Title GetMachines
// @Tag Machine API
// @Description get all machines
// @Param   pageSize     query    string  true        "The size of each page"
// @Param   p     query    string  true        "The number of the page"
// @Success 200 {object} object.Machine The Response object
// @router /get-machines [get]
func (c *ApiController) GetMachines() {
	owner := c.Ctx.Query("owner")
	limit := c.Ctx.Query("pageSize")
	page := c.Ctx.Query("p")
	field := c.Ctx.Query("field")
	value := c.Ctx.Query("value")
	sortField := c.Ctx.Query("sortField")
	sortOrder := c.Ctx.Query("sortOrder")

	_, err := object.SyncMachinesCloud(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if limit == "" || page == "" {
		machines, err := object.GetMaskedMachines(object.GetMachines(owner))
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(machines)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetMachineCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		offset, nums := util.Paginate(page, limit, count)
		machines, err := object.GetMaskedMachines(object.GetPaginationMachines(owner, offset, limit, field, value, sortField, sortOrder))
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(machines, nums)
	}
}

// GetMachine
// @Title GetMachine
// @Tag Machine API
// @Description get machine
// @Param   id     query    string  true        "The id ( owner/name ) of the machine"
// @Success 200 {object} object.Machine The Response object
// @router /get-machine [get]
func (c *ApiController) GetMachine() {
	id := c.Ctx.Query("id")

	owner, _ := util.GetOwnerAndNameFromId(id)
	_, err := object.SyncMachinesCloud(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	machine, err := object.GetMaskedMachine(object.GetMachine(id))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(machine)
}

// UpdateMachine
// @Title UpdateMachine
// @Tag Machine API
// @Description update machine
// @Param   id     query    string  true        "The id ( owner/name ) of the machine"
// @Param   body    body   object.Machine  true        "The details of the machine"
// @Success 200 {object} controllers.Response The Response object
// @router /update-machine [post]
func (c *ApiController) UpdateMachine() {
	id := c.Ctx.Query("id")

	var machine object.Machine
	err := json.Unmarshal(c.Ctx.Body(), &machine)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.UpdateMachine(id, &machine))
	c.ServeJSON()
}

// AddMachine
// @Title AddMachine
// @Tag Machine API
// @Description add a machine
// @Param   body    body   object.Machine  true        "The details of the machine"
// @Success 200 {object} controllers.Response The Response object
// @router /add-machine [post]
func (c *ApiController) AddMachine() {
	var machine object.Machine
	err := json.Unmarshal(c.Ctx.Body(), &machine)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.AddMachine(&machine))
	c.ServeJSON()
}

// DeleteMachine
// @Title DeleteMachine
// @Tag Machine API
// @Description delete a machine
// @Param   body    body   object.Machine  true        "The details of the machine"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-machine [post]
func (c *ApiController) DeleteMachine() {
	var machine object.Machine
	err := json.Unmarshal(c.Ctx.Body(), &machine)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.DeleteMachine(&machine))
	c.ServeJSON()
}

// LaunchMachine
// @Title LaunchMachine
// @Tag Machine API
// @Description launch a new cloud machine instance via a provider
// @Param   body    body   service.CreateMachineSpec  true  "The spec for the machine to launch"
// @Param   provider query string  true  "The provider name"
// @Success 200 {object} object.Machine The Response object
// @router /launch-machine [post]
func (c *ApiController) LaunchMachine() {
	// The org comes from the token, never from the request. This is a PROVISION:
	// it resolves the owner's provider credentials, spends against the owner's
	// balance and lands on the owner's invoice. It used to read `?owner=` while
	// the authorization filter judged the request BODY, so a body naming the
	// caller cleared authorization and a query naming somebody else decided whose
	// cloud account got charged.
	owner := c.resolveComputeOrg()
	if owner == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	provider := c.Ctx.Query("provider")
	if provider == "" {
		c.ResponseError("provider query parameter is required")
		return
	}

	var spec service.CreateMachineSpec
	err := json.Unmarshal(c.Ctx.Body(), &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	machine, err := object.CreateMachineCloud(owner, provider, &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(machine)
}
