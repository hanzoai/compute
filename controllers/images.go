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
		c.ResponseError(refuseNoOrg)
		return
	}
	if !service.ComputeConfigured() {
		c.ResponseError(refuseNoCompute)
		return
	}
	images, err := service.ListImages(org)
	if err != nil {
		// The rows that answered ride along with the named gap. This is a read a
		// caller makes while choosing what to launch, so a partial catalog that
		// says which part is missing serves them better than either an empty list
		// (which reads as "there are none") or a bare error (which hides the rest).
		c.ResponseError(err.Error(), images)
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
		c.ResponseError(refuseNoOrg)
		return
	}
	if !service.ComputeConfigured() {
		c.ResponseError(refuseNoCompute)
		return
	}
	var req struct {
		Name         string `json:"name"`
		Url          string `json:"url"`
		Region       string `json:"region"`
		Distribution string `json:"distribution"`
	}
	if err := json.Unmarshal(c.Ctx.Body(), &req); err != nil {
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
