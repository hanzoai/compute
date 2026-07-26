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
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

// GetNodePools
// @Title GetNodePools
// @Tag NodePool API
// @Description get all node pools
// @Param   owner     query    string  true        "The owner"
// @Param   pageSize     query    string  false        "The size of each page"
// @Param   p     query    string  false        "The number of the page"
// @Success 200 {object} object.NodePool The Response object
// @router /get-node-pools [get]
func (c *ApiController) GetNodePools() {
	owner := c.Ctx.Query("owner")
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
// @router /get-node-pool [get]
func (c *ApiController) GetNodePool() {
	id := c.Ctx.Query("id")

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
// @Param   owner     query    string  true  "The owner"
// @Param   provider  query    string  true  "The provider name"
// @Param   clusterId query    string  false "The DOKS cluster ID (optional, uses provider default)"
// @Param   body      body     service.CreateNodePoolSpec  true  "The spec for the node pool"
// @Success 200 {object} object.NodePool The Response object
// @router /create-node-pool [post]
func (c *ApiController) CreateNodePool() {
	owner := c.Ctx.Query("owner")
	provider := c.Ctx.Query("provider")
	clusterID := c.Ctx.Query("clusterId")

	if owner == "" || provider == "" {
		c.ResponseError("owner and provider query parameters are required")
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
// @router /update-node-pool [post]
func (c *ApiController) UpdateNodePool() {
	id := c.Ctx.Query("id")

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
// @router /delete-node-pool [post]
func (c *ApiController) DeleteNodePool() {
	var pool object.NodePool
	err := json.Unmarshal(c.Ctx.Body(), &pool)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.DeleteNodePoolCloud(&pool))
	c.ServeJSON()
}

// ScaleNodePool
// @Title ScaleNodePool
// @Tag NodePool API
// @Description quick-scale a node pool (just count)
// @Param   owner     query    string  true  "The owner"
// @Param   provider  query    string  true  "The provider name"
// @Param   clusterId query    string  false "The DOKS cluster ID"
// @Param   poolId    query    string  true  "The DOKS node pool ID"
// @Param   count     query    string  true  "The desired node count"
// @Success 200 {object} object.NodePool The Response object
// @router /scale-node-pool [post]
func (c *ApiController) ScaleNodePool() {
	owner := c.Ctx.Query("owner")
	provider := c.Ctx.Query("provider")
	clusterID := c.Ctx.Query("clusterId")
	poolID := c.Ctx.Query("poolId")
	countStr := c.Ctx.Query("count")

	if owner == "" || provider == "" || poolID == "" || countStr == "" {
		c.ResponseError("owner, provider, poolId, and count query parameters are required")
		return
	}

	count := util.ParseInt(countStr)
	if count < 0 {
		c.ResponseError("count must be a non-negative integer")
		return
	}

	pool, err := object.ScaleNodePoolCloud(owner, provider, clusterID, poolID, count)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(pool)
}
