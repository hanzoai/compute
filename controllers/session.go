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
	"github.com/hanzoai/visor/util"
)

// GetSessions
// @Title GetSessions
// @Tag Session API
// @Description get all sessions
// @Param   pageSize     query    string  true        "The size of each page"
// @Param   p     query    string  true        "The number of the page"
// @Success 200 {object} object.Session The Response object
// @router /get-sessions [get]
func (c *ApiController) GetSessions() {
	owner := c.Ctx.Query("owner")
	limit := c.Ctx.Query("pageSize")
	page := c.Ctx.Query("p")
	field := c.Ctx.Query("field")
	value := c.Ctx.Query("value")
	sortField := c.Ctx.Query("sortField")
	sortOrder := c.Ctx.Query("sortOrder")
	status := c.Ctx.Query("status")

	if limit == "" || page == "" {
		sessions, err := object.GetSessions(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(sessions)
	} else {
		limit := util.ParseInt(limit)

		count, err := object.GetSessionCount(owner, status, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		offset, nums := util.Paginate(page, limit, count)
		sessions, err := object.GetPaginationSessions(owner, status, offset, limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(sessions, nums)
	}
}

// GetConnSession
// @Title GetConnSession
// @Tag Session API
// @Description get session
// @Param   id     query    string  true        "The id of session"
// @Success 200 {object} object.Session
// @router /get-session [get]
func (c *ApiController) GetConnSession() {
	id := c.Ctx.Query("id")

	session, err := object.GetConnSession(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(session)
}

// DeleteSession
// @Title DeleteSession
// @Tag Session API
// @Description delete session
// @Param   id     query    string  true        "The id of session"
// @Success 200 {object} Response
// @router /delete-session [post]
func (c *ApiController) DeleteSession() {
	var session object.Session
	err := json.Unmarshal(c.Ctx.Body(), &session)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	affected, err := object.DeleteSession(&session)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(affected)
	c.ServeJSON()
}

// UpdateSession
// @Title UpdateSession
// @Tag Session API
// @Description update session
// @Param   id     query    string  true        "The id of session"
// @Param   body    body   object.Session true "The session object"
// @Success 200 {object} Response
// @router /update-session [post]
func (c *ApiController) UpdateSession() {
	id := c.Ctx.Query("id")

	var session object.Session
	err := json.Unmarshal(c.Ctx.Body(), &session)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.UpdateSession(id, &session))
	c.ServeJSON()
}

// AddSession
// @Title AddSession
// @Tag Session API
// @Description add session
// @Param   body    body   object.Session true "The session object"
// @Success 200 {object} Response
// @router /add-session [post]
func (c *ApiController) AddSession() {
	var session object.Session
	err := json.Unmarshal(c.Ctx.Body(), &session)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.AddSession(&session))
	c.ServeJSON()
}

func (c *ApiController) StartSession() {
	sessionId := c.Ctx.Query("id")

	s := &object.Session{
		Status:    object.Connected,
		StartTime: util.GetCurrentTime(),
	}

	_, err := object.UpdateSession(sessionId, s, []string{"status", "start_time"}...)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk()
}

func (c *ApiController) StopSession() {
	sessionId := c.Ctx.Query("id")

	err := object.CloseSession(sessionId, ForcedDisconnect, "The administrator forcibly closes the session")
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk()
}
