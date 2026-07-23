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
)

// GetVolumes
// @Title GetVolumes
// @Tag Volume API
// @Description get all volumes for an owner
// @Param   owner  query  string  true  "The owner"
// @Success 200 {array} object.Volume
// @router /get-volumes [get]
func (c *ApiController) GetVolumes() {
	owner := c.Ctx.Query("owner")
	volumes, err := object.GetVolumes(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(volumes)
}

// GetVolume
// @Title GetVolume
// @Tag Volume API
// @Description get a single volume
// @Param   owner  query  string  true  "The owner"
// @Param   name   query  string  true  "The volume name"
// @Success 200 {object} object.Volume
// @router /get-volume [get]
func (c *ApiController) GetVolume() {
	owner := c.Ctx.Query("owner")
	name := c.Ctx.Query("name")

	volume, err := object.GetVolume(owner, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(volume)
}

// CreateVolume
// @Title CreateVolume
// @Tag Volume API
// @Description create a block storage volume via a provider
// @Param   owner     query  string  true  "The owner"
// @Param   provider  query  string  true  "The provider name"
// @Param   body      body   service.CreateVolumeSpec  true  "The volume spec"
// @Success 200 {object} object.Volume
// @router /create-volume [post]
func (c *ApiController) CreateVolume() {
	owner := c.Ctx.Query("owner")
	providerName := c.Ctx.Query("provider")

	if owner == "" || providerName == "" {
		c.ResponseError("owner and provider query parameters are required")
		return
	}

	var spec service.CreateVolumeSpec
	err := json.Unmarshal(c.Ctx.Body(), &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	volume, err := object.CreateVolumeCloud(owner, providerName, &spec)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(volume)
}

// DeleteVolume
// @Title DeleteVolume
// @Tag Volume API
// @Description delete a volume
// @Param   owner     query  string  true  "The owner"
// @Param   provider  query  string  true  "The provider name"
// @Param   name      query  string  true  "The volume cloud ID"
// @Success 200 {object} controllers.Response
// @router /delete-volume [post]
func (c *ApiController) DeleteVolume() {
	owner := c.Ctx.Query("owner")
	providerName := c.Ctx.Query("provider")
	name := c.Ctx.Query("name")

	err := object.DeleteVolumeCloud(owner, providerName, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(true, nil)
	c.ServeJSON()
}

// AttachVolume
// @Title AttachVolume
// @Tag Volume API
// @Description attach a volume to a machine
// @router /attach-volume [post]
func (c *ApiController) AttachVolume() {
	owner := c.Ctx.Query("owner")
	providerName := c.Ctx.Query("provider")
	volumeName := c.Ctx.Query("volume")
	machineName := c.Ctx.Query("machine")

	err := object.AttachVolumeCloud(owner, providerName, volumeName, machineName)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(true, nil)
	c.ServeJSON()
}

// DetachVolume
// @Title DetachVolume
// @Tag Volume API
// @Description detach a volume from its machine
// @router /detach-volume [post]
func (c *ApiController) DetachVolume() {
	owner := c.Ctx.Query("owner")
	providerName := c.Ctx.Query("provider")
	volumeName := c.Ctx.Query("volume")

	err := object.DetachVolumeCloud(owner, providerName, volumeName)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(true, nil)
	c.ServeJSON()
}

// ResizeVolume
// @Title ResizeVolume
// @Tag Volume API
// @Description resize a volume
// @router /resize-volume [post]
func (c *ApiController) ResizeVolume() {
	owner := c.Ctx.Query("owner")
	providerName := c.Ctx.Query("provider")
	volumeName := c.Ctx.Query("volume")
	sizeStr := c.Ctx.Query("size")

	size := 0
	if sizeStr != "" {
		size = parseInt(sizeStr)
	}
	if size <= 0 {
		c.ResponseError("size parameter must be a positive integer (GB)")
		return
	}

	err := object.ResizeVolumeCloud(owner, providerName, volumeName, size)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(true, nil)
	c.ServeJSON()
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
