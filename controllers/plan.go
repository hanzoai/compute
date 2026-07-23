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

package controllers

import (
	"encoding/json"

	"github.com/hanzoai/visor/object"
)

// GetPlans
// @Title GetPlans
// @Tag Plan API
// @Description get active plans for a given owner
// @Param   owner  query  string  true  "The owner"
// @Success 200 {array} object.Plan
// @router /get-plans [get]
func (c *ApiController) GetPlans() {
	owner := c.Ctx.Query("owner")
	plans, err := object.GetPlans(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(plans)
}

// GetPlan
// @Title GetPlan
// @Tag Plan API
// @Description get a single plan
// @Param   owner  query  string  true  "The owner"
// @Param   name   query  string  true  "The plan name"
// @Success 200 {object} object.Plan
// @router /get-plan [get]
func (c *ApiController) GetPlan() {
	owner := c.Ctx.Query("owner")
	name := c.Ctx.Query("name")

	plan, err := object.GetPlan(owner, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(plan)
}

// AddPlan
// @Title AddPlan
// @Tag Plan API
// @Description add a plan
// @Param   body  body  object.Plan  true  "The plan"
// @Success 200 {object} controllers.Response
// @router /add-plan [post]
func (c *ApiController) AddPlan() {
	var plan object.Plan
	err := json.Unmarshal(c.Ctx.Body(), &plan)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.Data["json"] = wrapActionResponse(object.AddPlan(&plan))
	c.ServeJSON()
}

// UpdatePlan
// @Title UpdatePlan
// @Tag Plan API
// @Description update a plan
// @Param   owner  query  string  true  "The owner"
// @Param   name   query  string  true  "The plan name"
// @Param   body   body   object.Plan  true  "The plan"
// @Success 200 {object} controllers.Response
// @router /update-plan [post]
func (c *ApiController) UpdatePlan() {
	owner := c.Ctx.Query("owner")
	name := c.Ctx.Query("name")

	var plan object.Plan
	err := json.Unmarshal(c.Ctx.Body(), &plan)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.Data["json"] = wrapActionResponse(object.UpdatePlan(owner, name, &plan))
	c.ServeJSON()
}

// DeletePlan
// @Title DeletePlan
// @Tag Plan API
// @Description delete a plan
// @Param   body  body  object.Plan  true  "The plan"
// @Success 200 {object} controllers.Response
// @router /delete-plan [post]
func (c *ApiController) DeletePlan() {
	var plan object.Plan
	err := json.Unmarshal(c.Ctx.Body(), &plan)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.Data["json"] = wrapActionResponse(object.DeletePlan(&plan))
	c.ServeJSON()
}
