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

	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/util"
)

// GetProviders
// @Title GetProviders
// @Tag Provider API
// @Description get all providers
// @Param   pageSize     query    string  true        "The size of each page"
// @Param   p     query    string  true        "The number of the page"
// @Success 200 {object} object.Provider The Response object
// @router /get-providers [get]
func (c *ApiController) GetProviders() {
	owner := c.Ctx.Query("owner")
	limit := c.Ctx.Query("pageSize")
	page := c.Ctx.Query("p")
	field := c.Ctx.Query("field")
	value := c.Ctx.Query("value")
	sortField := c.Ctx.Query("sortField")
	sortOrder := c.Ctx.Query("sortOrder")

	if limit == "" || page == "" {
		providers, err := object.GetMaskedProviders(object.GetProviders(owner))
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(providers)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetProviderCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		offset, nums := util.Paginate(page, limit, count)
		providers, err := object.GetMaskedProviders(object.GetPaginationProviders(owner, offset, limit, field, value, sortField, sortOrder))
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(providers, nums)
	}
}

// GetProvider
// @Title GetProvider
// @Tag Provider API
// @Description get provider
// @Param   id     query    string  true        "The id ( owner/name ) of the provider"
// @Success 200 {object} object.Provider The Response object
// @router /get-provider [get]
func (c *ApiController) GetProvider() {
	id := c.Id()

	provider, err := object.GetMaskedProvider(object.GetProvider(id))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(provider)
}

// UpdateProvider
// @Title UpdateProvider
// @Tag Provider API
// @Description update provider
// @Param   id     query    string  true        "The id ( owner/name ) of the provider"
// @Param   body    body   object.Provider  true        "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /update-provider [post]
func (c *ApiController) UpdateProvider() {
	id := c.Id()

	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Body(), &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.UpdateProvider(id, &provider))
	c.ServeJSON()
}

// AddProvider
// @Title AddProvider
// @Tag Provider API
// @Description add a provider
// @Param   body    body   object.Provider  true        "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /add-provider [post]
func (c *ApiController) AddProvider() {
	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Body(), &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.AddProvider(&provider))
	c.ServeJSON()
}

// DeleteProvider
// @Title DeleteProvider
// @Tag Provider API
// @Description delete a provider
// @Param   body    body   object.Provider  true        "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-provider [post]
func (c *ApiController) DeleteProvider() {
	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Body(), &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.DeleteProvider(&provider))
	c.ServeJSON()
}

// AddProviderKey
// @Title AddProviderKey
// @Tag Provider API
// @Description add a rotation key to a provider
// @Param   owner   path   string           true   "The provider owner"
// @Param   name    path   string           true   "The provider name"
// @Param   body    body   object.ProviderKey  true   "The rotation key"
// @Success 200 {object} controllers.Response The Response object
// @router /providers/{owner}/{name}/keys [post]
func (c *ApiController) AddProviderKey() {
	id := c.Id()

	var key object.ProviderKey
	if err := json.Unmarshal(c.Ctx.Body(), &key); err != nil {
		c.ResponseError(err.Error())
		return
	}

	affected, err := object.AddProviderKey(id, key)
	if err == nil {
		logs.Info("audit: provider %s key %q added by %s", id, key.Name, c.GetSessionUsername())
	}
	c.Data["json"] = wrapActionResponse(affected, err)
	c.ServeJSON()
}

// RotateProviderKey
// @Title RotateProviderKey
// @Tag Provider API
// @Description rotate a provider rotation key's secret, or set its state
// @Param   owner    path   string           true   "The provider owner"
// @Param   name     path   string           true   "The provider name"
// @Param   keyName  path   string           true   "The rotation key name"
// @Param   body     body   object.ProviderKey  true   "The new secret and/or state"
// @Success 200 {object} controllers.Response The Response object
// @router /providers/{owner}/{name}/keys/{keyName} [put]
func (c *ApiController) RotateProviderKey() {
	id := c.Id()
	keyName := c.Ctx.Param("keyName")

	var in object.ProviderKey
	if err := json.Unmarshal(c.Ctx.Body(), &in); err != nil {
		c.ResponseError(err.Error())
		return
	}

	affected, err := object.RotateProviderKey(id, keyName, in)
	if err == nil {
		logs.Info("audit: provider %s key %q rotated (state=%q) by %s", id, keyName, in.State, c.GetSessionUsername())
	}
	c.Data["json"] = wrapActionResponse(affected, err)
	c.ServeJSON()
}

// DeleteProviderKey
// @Title DeleteProviderKey
// @Tag Provider API
// @Description remove a rotation key from a provider
// @Param   owner    path   string   true   "The provider owner"
// @Param   name     path   string   true   "The provider name"
// @Param   keyName  path   string   true   "The rotation key name"
// @Success 200 {object} controllers.Response The Response object
// @router /providers/{owner}/{name}/keys/{keyName} [delete]
func (c *ApiController) DeleteProviderKey() {
	id := c.Id()
	keyName := c.Ctx.Param("keyName")

	affected, err := object.DeleteProviderKey(id, keyName)
	if err == nil {
		logs.Info("audit: provider %s key %q removed by %s", id, keyName, c.GetSessionUsername())
	}
	c.Data["json"] = wrapActionResponse(affected, err)
	c.ServeJSON()
}

// VerifyProvider
// @Title VerifyProvider
// @Tag Provider API
// @Description dry-run validate a provider's stored credential (creates nothing)
// @Param   owner    path   string   true   "The provider owner"
// @Param   name     path   string   true   "The provider name"
// @Param   keyName  query  string   false  "Rotation key to test; omit for the row's own credential"
// @Success 200 {object} controllers.Response The Response object
// @router /providers/{owner}/{name}/verify [post]
func (c *ApiController) VerifyProvider() {
	id := c.Id()
	account := c.Ctx.Query("keyName")

	ok, detail, err := object.VerifyProvider(id, account)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(map[string]interface{}{"ok": ok, "detail": detail})
}
