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

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// RECORDS — the audit trail, one address per resource:
//
//	GET    /v1/records                       list (?owner, paging, filter)
//	POST   /v1/records                       create
//	GET    /v1/records/:owner/:name          read
//	PUT    /v1/records/:owner/:name          replace
//	DELETE /v1/records/:owner/:name          remove
//	PUT    /v1/records/:owner/:name/block    write the record into a chain block
//	GET    /v1/records/:owner/:name/block    read that block back
//
// A record's id is `owner/name`, so the item address spells both segments and
// nothing else needs to. `?id=owner/name` and the record-shaped request bodies
// are gone: they let the URL, the query and the body each name a different row,
// and the authorizer read one while the store wrote another.

// recordId is the record the item address names: /v1/records/<owner>/<name>.
// The id IS the address, so the handler and the authorizer (routers.getObject)
// resolve the same two segments — a query parameter and a body field can name
// two different rows, and then one of them is the one that gets written.
func (c *ApiController) recordId() string {
	return util.GetIdFromOwnerAndName(c.Ctx.Param("owner"), c.Ctx.Param("name"))
}

// GetRecords
// @Title GetRecords
// @Tag Record API
// @Description list records
// @Param   owner     query    string  false       "The org whose records to list"
// @Param   pageSize     query    string  true        "The size of each page"
// @Param   p     query    string  true        "The number of the page"
// @Success 200 {object} object.Record The Response object
// @router /records [get]
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
// @Description read one record
// @Param   owner     path    string  true        "The org that owns the record"
// @Param   name     path    string  true        "The record's name"
// @Success 200 {object} object.Record The Response object
// @router /records/{owner}/{name} [get]
func (c *ApiController) GetRecord() {
	record, err := object.GetRecord(c.recordId())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(record)
}

// UpdateRecord
// @Title UpdateRecord
// @Tag Record API
// @Description replace a record
// @Param   owner     path    string  true        "The org that owns the record"
// @Param   name     path    string  true        "The record's name"
// @Param   body    body   object.Record  true        "The details of the record"
// @Success 200 {object} controllers.Response The Response object
// @router /records/{owner}/{name} [put]
func (c *ApiController) UpdateRecord() {
	id := c.recordId()

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
// @router /records [post]
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
// @Description remove a record
// @Param   owner     path    string  true        "The org that owns the record"
// @Param   name     path    string  true        "The record's name"
// @Success 200 {object} controllers.Response The Response object
// @router /records/{owner}/{name} [delete]
func (c *ApiController) DeleteRecord() {
	// The address names the row, so nothing is read from the body. It used to be
	// the only place the row was named, which meant the caller could hand the
	// authorizer one owner and the store another.
	owner, name := c.Ctx.Param("owner"), c.Ctx.Param("name")
	c.Data["json"] = wrapActionResponse(object.DeleteRecord(&object.Record{Owner: owner, Name: name}))
	c.ServeJSON()
}
