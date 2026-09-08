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
	"strings"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/util"
)

type Response struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   interface{} `json:"data"`
	Data2  interface{} `json:"data2"`
}

// serve writes payload as the JSON response (HTTP 200, the SDK contract branches
// on the envelope status, not the code) and stashes it on the request context so
// the record filter can capture the response envelope after the handler returns.
func (c *ApiController) serve(payload interface{}) {
	if c.Ctx == nil {
		return
	}
	c.Ctx.Locals(object.RecordResponseKey, payload)
	_ = c.Ctx.JSON(200, payload)
}

// ServeJSON writes the buffered Data["json"] payload — the Beego-shaped spelling
// the handlers use for the wrapActionResponse path.
func (c *ApiController) ServeJSON() {
	c.serve(c.Data["json"])
}

func (c *ApiController) ResponseOk(data ...interface{}) {
	resp := Response{Status: "ok"}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	c.serve(resp)
}

func (c *ApiController) ResponseError(error string, data ...interface{}) {
	resp := Response{Status: "error", Msg: error}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	c.serve(resp)
}

func (c *ApiController) RequireSignedIn() bool {
	if c.GetSessionUser() == nil {
		c.ResponseError(refuseNotSignedIn)
		return true
	}

	return false
}

func (c *ApiController) getClientIp() string {
	res := strings.Replace(util.ClientIPFromCtx(c.Ctx), ": ", "", -1)
	return res
}

func (c *ApiController) getUserAgent() string {
	return c.Ctx.Header("User-Agent")
}
