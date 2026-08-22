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

// node_pool.go serves a node pool's lifecycle, and every handler here takes its
// tenant from ONE place: resolveComputeOrg, the same principal rule the machine
// and cluster surfaces run.
//
// It used to take it from two. The authorization filter derives the object's
// owner from `?id=` or the request BODY, while these handlers read
// `?owner=` — so `POST /v1/pools?owner=hanzo` with a body naming
// `acme` cleared authorization against acme and then provisioned against hanzo's
// cloud credentials, hanzo's balance and hanzo's invoice. A tenant read from a
// different field than the one authorization judged is not a second opinion, it
// is a configured cloud account.
package controllers

import (
	"encoding/json"
	"strings"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

// poolId resolves a request into the fully-qualified `owner/name` node-pool id it
// addresses: the OWNER is always the caller's own org and the NAME is whatever
// the request named. A client may still send the historical `owner/name` form —
// only its name half is read, so an id naming another org addresses nothing but
// the caller's own pool of that name.
//
// It is the node-pool twin of `machine` (agent_binding.go): the id a caller can
// build is always scoped to its own org.
func poolId(org, id string) string {
	name := strings.TrimSpace(id)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if org == "" || name == "" {
		return ""
	}
	return util.GetIdFromOwnerAndName(org, name)
}

// GetNodePools
// @Title GetNodePools
// @Tag NodePool API
// @Description get all node pools
// @Param   pageSize     query    string  false        "The size of each page"
// @Param   p     query    string  false        "The number of the page"
// @Success 200 {object} object.NodePool The Response object
// @router /pools [get]
func (c *ApiController) GetNodePools() {
	owner := c.resolveComputeOrg()
	if owner == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	limit := c.Ctx.Query("pageSize")
	page := c.Ctx.Query("p")
	field := c.Ctx.Query("field")
	value := c.Ctx.Query("value")
	sortField := c.Ctx.Query("sortField")
	sortOrder := c.Ctx.Query("sortOrder")

	_, err := object.SyncNodePoolsCloud(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if limit == "" || page == "" {
		pools, err := object.GetNodePools(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(pools)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetNodePoolCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		offset, nums := util.Paginate(page, limit, count)
		pools, err := object.GetPaginationNodePools(owner, offset, limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(pools, nums)
	}
}

// GetNodePool
// @Title GetNodePool
// @Tag NodePool API
// @Description get a node pool
// @Param   id     query    string  true        "The id ( owner/name ) of the node pool"
// @Success 200 {object} object.NodePool The Response object
// @router /pools/{owner}/{name} [get]
func (c *ApiController) GetNodePool() {
	id := poolId(c.resolveComputeOrg(), c.Id())
	if id == "" {
		c.ResponseError(refuseNoOrg)
		return
	}

	pool, err := object.GetNodePool(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(pool)
}

// CreateNodePool
// @Title CreateNodePool
// @Tag NodePool API
// @Description create a new node pool in DOKS
// @Param   provider  query    string  true  "The provider name"
// @Param   clusterId query    string  false "The DOKS cluster ID (optional, uses provider default)"
// @Param   body      body     service.CreateNodePoolSpec  true  "The spec for the node pool"
// @Success 200 {object} object.NodePool The Response object
// @router /pools [post]
func (c *ApiController) CreateNodePool() {
	owner := c.resolveComputeOrg()
	if owner == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	provider := c.Ctx.Query("provider")
	clusterID := c.Ctx.Query("clusterId")

	if provider == "" {
		c.ResponseError(refuseNoProviderName)
		return
	}

	var spec service.CreateNodePoolSpec
	err := json.Unmarshal(c.Ctx.Body(), &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	pool, err := object.CreateNodePoolCloud(owner, provider, clusterID, &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(pool)
}

// UpdateNodePool
// @Title UpdateNodePool
// @Tag NodePool API
// @Description update a node pool
// @Param   id     query    string  true        "The id ( owner/name ) of the node pool"
// @Param   body    body   object.NodePool  true        "The details of the node pool"
// @Success 200 {object} controllers.Response The Response object
// @router /pools/{owner}/{name} [put]
func (c *ApiController) UpdateNodePool() {
	id := poolId(c.resolveComputeOrg(), c.Id())
	if id == "" {
		c.ResponseError(refuseNoOrg)
		return
	}

	var pool object.NodePool
	err := json.Unmarshal(c.Ctx.Body(), &pool)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.UpdateNodePool(id, &pool))
	c.ServeJSON()
}

// DeleteNodePool
// @Title DeleteNodePool
// @Tag NodePool API
// @Description delete a node pool from DOKS and DB
// @Param   body    body   object.NodePool  true        "The details of the node pool"
// @Success 200 {object} controllers.Response The Response object
// @router /pools/{owner}/{name} [delete]
func (c *ApiController) DeleteNodePool() {
	owner := c.resolveComputeOrg()
	if owner == "" {
		c.ResponseError(refuseNoOrg)
		return
	}

	// The ADDRESS names WHICH pool; the token names WHOSE. DeleteNodePoolCloud
	// reads only those two and takes every other field from the stored row, so
	// there is nothing left for a body to carry.
	name := strings.TrimSpace(c.Ctx.Param("name"))
	if name == "" {
		c.ResponseError(refuseNoPool)
		return
	}
	pool := object.NodePool{Owner: owner, Name: name}

	c.Data["json"] = wrapActionResponse(object.DeleteNodePoolCloud(&pool))
	c.ServeJSON()
}

// ScaleNodePool
// @Title ScaleNodePool
// @Tag NodePool API
// @Description quick-scale a node pool (just count)
// @Param   provider  query    string  true  "The provider name"
// @Param   clusterId query    string  false "The DOKS cluster ID"
// @Param   poolId    query    string  true  "The DOKS node pool ID"
// @Param   count     query    string  true  "The desired node count"
// @Success 200 {object} object.NodePool The Response object
// @router /pools/{owner}/{name}/size [put]
func (c *ApiController) ScaleNodePool() {
	owner := c.resolveComputeOrg()
	if owner == "" {
		c.ResponseError(refuseNoOrg)
		return
	}
	// The address names the pool; the provider, the cluster and the upstream pool
	// id are on the row visor already keeps. A caller that has to repeat them can
	// contradict them.
	name := strings.TrimSpace(c.Ctx.Param("name"))
	countStr := c.Ctx.Query("count")
	if name == "" || countStr == "" {
		c.ResponseError(refuseNoPool)
		return
	}

	count := util.ParseInt(countStr)
	if count < 0 {
		c.ResponseError("count must be a non-negative integer")
		return
	}

	stored, err := object.GetNodePool(owner + "/" + name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if stored == nil {
		c.ResponseError("no pool " + name + " in " + owner)
		return
	}

	pool, err := object.ScaleNodePoolCloud(owner, stored.Provider, stored.ClusterID, stored.PoolID, count)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(pool)
}
