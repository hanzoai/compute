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
)

// UpdateMachine replaces one machine.
//
// @Title UpdateMachine
// @Tag Machine API
// @Param   owner  path  string  true  "The organization"
// @Param   name   path  string  true  "The machine's name"
// @Param   body   body  object.Machine  true  "The machine"
// @Success 200 {object} controllers.Response The Response object
// @router /machines/{owner}/{name} [put]
func (c *ApiController) UpdateMachine() {
	id := c.Id()

	var machine object.Machine
	err := json.Unmarshal(c.Ctx.Body(), &machine)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.UpdateMachine(id, &machine))
	c.ServeJSON()
}

// DeleteMachine removes one machine.
//
// @Title DeleteMachine
// @Tag Machine API
// @Param   owner  path  string  true  "The organization"
// @Param   name   path  string  true  "The machine's name"
// @Success 200 {object} controllers.Response The Response object
// @router /machines/{owner}/{name} [delete]
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
