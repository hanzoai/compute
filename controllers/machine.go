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
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
)

// UpdateMachine replaces one machine.
//
// A hosted machine has one writable property, its state: "Running" starts it
// (a metered start) and "Stopped" stops it. Any other machine is a row of the
// organization's own providers, replaced as a whole.
//
// @Title UpdateMachine
// @Tag Machine API
// @Param   owner  path  string  true  "The organization"
// @Param   name   path  string  true  "The machine's name"
// @Param   body   body  object.Machine  true  "The machine"
// @Success 200 {object} controllers.Response The Response object
// @router /machines/{owner}/{name} [put]
func (c *ApiController) UpdateMachine() {
	var machine object.Machine
	err := json.Unmarshal(c.Ctx.Body(), &machine)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if service.ComputeConfigured() {
		org, name := c.resolveComputeOrg(), strings.TrimSpace(c.Ctx.Param("name"))
		if org == "" || name == "" {
			c.ResponseError(refuseNoOrg)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		affected, err := service.SetOrgMachineState(ctx, org, name, machine.State)
		if !errors.Is(err, service.ErrNoMachine) {
			c.Data["json"] = wrapActionResponse(affected, err)
			c.ServeJSON()
			return
		}
	}

	c.Data["json"] = wrapActionResponse(object.UpdateMachine(c.Id(), &machine))
	c.ServeJSON()
}

// DeleteMachine removes one machine: a hosted machine is terminated, a row of
// the organization's own providers is dropped. The machine is the one the
// address names, in the caller's organization; the request carries no body.
//
// @Title DeleteMachine
// @Tag Machine API
// @Param   owner  path  string  true  "The organization"
// @Param   name   path  string  true  "The machine's name"
// @Success 200 {object} controllers.Response The Response object
// @router /machines/{owner}/{name} [delete]
func (c *ApiController) DeleteMachine() {
	org, name := c.resolveComputeOrg(), strings.TrimSpace(c.Ctx.Param("name"))
	if org == "" || name == "" {
		c.ResponseError(refuseNoOrg)
		return
	}

	if service.ComputeConfigured() {
		err := service.DeleteOrgMachine(org, name)
		if !errors.Is(err, service.ErrNoMachine) {
			c.Data["json"] = wrapActionResponse(err == nil, err)
			c.ServeJSON()
			return
		}
	}

	c.Data["json"] = wrapActionResponse(object.DeleteMachine(&object.Machine{Owner: org, Name: name}))
	c.ServeJSON()
}
