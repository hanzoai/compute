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

package controllers

import (
	"encoding/json"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/util"
)

// GetRecords
// @Title GetRecords
// @Tag Record API
// @Description get all records
// @Param   pageSize     query    string  true        "The size of each page"
// @Param   p     query    string  true        "The number of the page"
// @Success 200 {object} object.Record The Response object
// @router /get-records [get]
func (c *ApiController) GetRecords() {
	owner := c.Ctx.Query("owner")
	limit := c.Ctx.Query("pageSize")
	page := c.Ctx.Query("p")
	field := c.Ctx.Query("field")
	value := c.Ctx.Query("value")
	sortField := c.Ctx.Query("sortField")
	sortOrder := c.Ctx.Query("sortOrder")

	if limit == "" || page == "" {
		records, err := object.GetRecords(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(records)
	} else {
		limit := util.ParseInt(limit)

		count, err := object.GetRecordCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		offset, nums := util.Paginate(page, limit, count)
		records, err := object.GetPaginationRecords(owner, offset, limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(records, nums)
	}
}

// GetRecord
// @Title GetRecord
// @Tag Record API
// @Description get record
// @Param   id     query    string  true        "The id ( owner/name ) of the record"
// @Success 200 {object} object.Record The Response object
// @router /get-record [get]
func (c *ApiController) GetRecord() {
	id := c.Id()

	record, err := object.GetRecord(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(record)
}

// UpdateRecord
// @Title UpdateRecord
// @Tag Record API
// @Description update record
// @Param   id     query    string  true        "The id ( owner/name ) of the record"
// @Param   body    body   object.Record  true        "The details of the record"
// @Success 200 {object} controllers.Response The Response object
// @router /update-record [post]
func (c *ApiController) UpdateRecord() {
	id := c.Id()

	var record object.Record
	err := json.Unmarshal(c.Ctx.Body(), &record)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.UpdateRecord(id, &record))
	c.ServeJSON()
}

// AddRecord
// @Title AddRecord
// @Tag Record API
// @Description add a record
// @Param   body    body   object.Record  true        "The details of the record"
// @Success 200 {object} controllers.Response The Response object
// @router /add-record [post]
func (c *ApiController) AddRecord() {
	var record object.Record
	err := json.Unmarshal(c.Ctx.Body(), &record)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if record.ClientIp == "" {
		record.ClientIp = c.getClientIp()
	}
	if record.UserAgent == "" {
		record.UserAgent = c.getUserAgent()
	}

	c.Data["json"] = wrapActionResponse(object.AddRecord(&record))
	c.ServeJSON()
}

// DeleteRecord
// @Title DeleteRecord
// @Tag Record API
// @Description delete a record
// @Param   body    body   object.Record  true        "The details of the record"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-record [post]
func (c *ApiController) DeleteRecord() {
	var record object.Record
	err := json.Unmarshal(c.Ctx.Body(), &record)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.DeleteRecord(&record))
	c.ServeJSON()
}
