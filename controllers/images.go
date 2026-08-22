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
	"context"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/service"
)

// images.go — the /v1/images surface: browse selectable images and upload your
// own. Every machine launch request accepts the returned slug (distribution) or id
// (custom), so a caller can customize the image step of every machine.
//
// ONE noun, ONE address, the METHOD carrying the verb, and both ops TYPED — so
// this collection is in the registry the OpenAPI document, the MCP tool list, the
// CLI and every generated SDK are built from, rather than only on the wire.

// Images is the catalog an org may launch from: shared distributions, 1-click
// applications, and that org's own custom uploads.
//
// The field is ALWAYS an array on the wire, never null and never absent — the
// same rule Nodes states, for the same reason.
type Images struct {
	// Images is one row per selectable image.
	Images []service.ImageInfo `json:"images"`
	// Missing names the parts of the catalog that did not answer, and is absent
	// when all of them did. A read that half-worked is neither a failure nor a
	// clean answer: the rows that came back are worth having while choosing what
	// to launch, and an empty list is a legitimate answer, so it can never also
	// be how a failure is reported.
	Missing string `json:"missing,omitempty"`
}

// ImageDraft registers a custom image from a URL into the configured cloud
// account, tagged to the caller's org so only that org sees it.
//
// Every field is body-only (`url:"-"`): the binder fills an input from the query
// string as well as the body, so without it a `?name=` would outrank what the
// caller actually sent and redirect the write.
type ImageDraft struct {
	// Name is what the image is called in this org's catalog.
	Name string `json:"name" url:"-"`
	// Url is where the image file is fetched from.
	Url string `json:"url" url:"-"`
	// Region is where the image is registered; it can only be launched there.
	Region string `json:"region" url:"-"`
	// Distribution is the base OS the image carries, when the caller knows it.
	Distribution string `json:"distribution" url:"-"`
	caller
}

// ListImages returns what the caller's org may launch: shared distributions and
// 1-click applications, plus that org's OWN custom images. One tenant never sees
// another's uploads even though they share the configured cloud account.
//
// Response: {"images": [{"slug": "ubuntu-24-04-x64", "name": "Ubuntu 24.04", "kind": "distribution"}]}
func ListImages(_ context.Context, in *Scope) (*Images, error) {
	_, org := principal(in.Authorization, in.Owner)
	if org == "" {
		return nil, zip.ErrForbidden("no org context")
	}
	if !service.ComputeConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "hanzo compute is not configured")
	}
	images, err := service.ListImages(org)
	if images == nil {
		images = []service.ImageInfo{}
	}
	out := &Images{Images: images}
	if err != nil {
		out.Missing = err.Error()
	}
	return out, nil
}

// CreateImage registers a custom image from a URL into the caller org's catalog.
//
// Creation is asynchronous: the image answers with status "pending" and becomes
// launchable by its returned id once it reads "available".
//
// Example: {"name": "base-2404", "url": "https://example.test/base.qcow2", "region": "nyc3"}
// Response: {"id": 1234, "name": "base-2404", "kind": "custom", "status": "pending"}
func CreateImage(_ context.Context, in *ImageDraft) (*service.ImageInfo, error) {
	_, org := principal(in.Authorization, in.Owner)
	if org == "" {
		return nil, zip.ErrForbidden("no org context")
	}
	if !service.ComputeConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "hanzo compute is not configured")
	}
	img, err := service.CreateOrgImage(org, in.Name, in.Url, in.Region, in.Distribution)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return img, nil
}
