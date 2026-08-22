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
	"fmt"

	"github.com/hanzoai/visor/object"
)

// A record's BLOCK is its copy on the chain: the block and transaction ids that
// object.CommitRecord writes back onto the row, and the chain's own answer when
// that block is read again. `commit` and `query` were two verbs for that one
// sub-resource, so it is one address and the method carries the verb.

// CommitRecord
// @Title CommitRecord
// @Tag Record API
// @Description write a record into a chain block
// @Param   owner     path    string  true        "The org that owns the record"
// @Param   name     path    string  true        "The record's name"
// @Success 200 {object} controllers.Response The Response object
// @router /records/{owner}/{name}/block [put]
func (c *ApiController) CommitRecord() {
	// The STORED record is what goes on the chain. It used to be whatever record
	// the caller put in the body, which anchored client-supplied content under a
	// stored row's id — the opposite of what anchoring an audit record is for.
	id := c.recordId()
	record, err := object.GetRecord(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if record == nil {
		c.ResponseError(fmt.Sprintf("the record: %s does not exist", id))
		return
	}

	c.Data["json"] = wrapActionResponse(object.CommitRecord(record))
	c.ServeJSON()
}

// QueryRecord
// @Title QueryRecord
// @Tag Record API
// @Description read a record's chain block back
// @Param   owner     path    string  true        "The org that owns the record"
// @Param   name     path    string  true        "The record's name"
// @Success 200 {object} object.Record The Response object
// @router /records/{owner}/{name}/block [get]
func (c *ApiController) QueryRecord() {
	res, err := object.QueryRecord(c.recordId())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(res)
}
