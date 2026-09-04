// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package controllers

import (
	"strings"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
)

// A machine is a machine, wherever it runs.
//
// There were two collections. One read a table compute rebuilds from the
// organization's OWN provider credentials; the other read the cloud API on the
// house account. A caller wanting "the machines I have" had to ask both and
// join them — hanzoai/cloud did exactly that, into variables named registry and
// live, and any caller that asked only one got a partial answer without being
// told so.
//
// They were never two kinds of thing. object.Machine and service.Machine both
// declare (Owner, Name) as their primary key and both carry Id as an ordinary
// field holding the PROVIDER'S id. So the identity was always the same, and
// only the source differed — which is a property of a machine, not a reason for
// a second address.
//
// Source says which. Registry wins a collision, because a machine compute has a
// row for is one it knows more about than the cloud listing does.
const (
	sourceRegistry = "registry"
	sourceLive     = "live"
)

// machines returns every machine the organization has, from both sources,
// keyed the one way.
func (c *ApiController) machines(org string) ([]*object.Machine, error) {
	if _, err := object.SyncMachinesCloud(org); err != nil {
		return nil, err
	}
	rows, err := object.GetMaskedMachines(object.GetMachines(org))
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rows))
	for _, m := range rows {
		m.Source = sourceRegistry
		seen[m.Owner+"/"+m.Name] = true
	}
	// The house account is a second source, not a second collection. It is read
	// only when it is configured, and a failure there does not lose the rows
	// that were already found — a partial answer is what having two addresses
	// used to force on every caller.
	if service.ComputeConfigured() {
		live, err := service.ListOrgMachines(org, c.resolveComputeProject(""))
		if err == nil {
			for _, m := range live {
				if seen[m.Owner+"/"+m.Name] {
					continue
				}
				rows = append(rows, &object.Machine{
					Owner: m.Owner, Name: m.Name, Id: m.Id, Provider: m.Provider,
					Region: m.Region, Zone: m.Zone, Category: m.Category, Type: m.Type,
					Size: m.Size, State: m.State, Tag: m.Tag,
					CreatedTime: m.CreatedTime, ExpireTime: m.ExpireTime,
					DisplayName: m.DisplayName, Source: sourceLive,
				})
			}
		}
	}
	return rows, nil
}

// ListMachines answers the organization's machines.
//
// @Title ListMachines
// @Tag Machine API
// @Description Every machine the organization has, from its own providers and
// @Description from the house account. Source says which.
// @Param   owner  query  string  false  "The organization, for a service caller"
// @Success 200 {object} Response
// @router /machines [get]
func (c *ApiController) ListMachines() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	rows, err := c.machines(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(rows)
}

// GetMachine answers one machine by the key it has always had.
//
// @Title GetMachine
// @Tag Machine API
// @Description One machine, addressed by the organization that owns it and its name.
// @Param   owner  path  string  true  "The organization"
// @Param   name   path  string  true  "The machine's name"
// @Success 200 {object} Response
// @router /machines/{owner}/{name} [get]
func (c *ApiController) GetMachine() {
	// The address names WHICH machine; the token names WHOSE. Reading the owner
	// straight off the path would let a caller name any org and be answered from
	// it — resolveComputeOrg runs the same segment through the principal, which
	// is what every other compute read does and the only reason they are scoped.
	org := c.resolveComputeOrg()
	name := strings.TrimSpace(c.Ctx.Param("name"))
	if org == "" || name == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	rows, err := c.machines(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	for _, m := range rows {
		if m.Name == name {
			c.ResponseOk(m)
			return
		}
	}
	c.ResponseError("no machine " + name + " in " + org)
}
