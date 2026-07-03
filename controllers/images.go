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

	"github.com/hanzoai/visor/service"
)

// images.go — the /v1/images surface: browse selectable images and upload your
// own. Every machine launch request accepts the returned slug (distribution) or id
// (custom), so a caller can customize the image step of every machine.

// ListImages
// @Title ListImages
// @Tag Compute API
// @Description list selectable images (distributions, applications, org customs)
// @router /images [get]
func (c *ApiController) ListImages() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}
	images, err := service.ListImages(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(images)
}

// CreateImage
// @Title CreateImage
// @Tag Compute API
// @Description upload/register a custom image from a URL (org-scoped, async)
// @router /images [post]
func (c *ApiController) CreateImage() {
	org := c.resolveComputeOrg()
	if org == "" {
		c.ResponseError("unauthorized: no org context")
		return
	}
	if !service.ComputeConfigured() {
		c.ResponseError("hanzo compute is not configured")
		return
	}
	var req struct {
		Name         string `json:"name"`
		Url          string `json:"url"`
		Region       string `json:"region"`
		Distribution string `json:"distribution"`
	}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	img, err := service.CreateOrgImage(org, req.Name, req.Url, req.Region, req.Distribution)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(img)
}
