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

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/digitalocean/godo"
)

// images.go — selectable machine images for the /v1 resell-compute surface.
// Provider identity stays internal; a caller selects a shared distribution or
// 1-click application by Slug, or its OWN uploaded custom image by ID. Custom
// images are org-scoped: CreateOrgImage tags each with the owning org and
// ListImages only returns the caller org's own customs, so one tenant never
// sees another's uploads even though they share Hanzo's house DO account.

// ImageInfo is one selectable image.
type ImageInfo struct {
	ID           int      `json:"id,omitempty"`   // custom/app images select by ID
	Slug         string   `json:"slug,omitempty"` // distributions select by slug
	Name         string   `json:"name"`
	Distribution string   `json:"distribution,omitempty"`
	Kind         string   `json:"kind"` // "distribution" | "application" | "custom"
	Regions      []string `json:"regions,omitempty"`
	MinDiskGB    int      `json:"minDiskGb,omitempty"`
	SizeGB       float64  `json:"sizeGb,omitempty"`
	Status       string   `json:"status,omitempty"` // custom: "pending" -> "available"
}

func imageInfo(i godo.Image, kind string) ImageInfo {
	return ImageInfo{
		ID: i.ID, Slug: i.Slug, Name: i.Name, Distribution: i.Distribution,
		Kind: kind, Regions: i.Regions, MinDiskGB: i.MinDiskSize,
		SizeGB: i.SizeGigaBytes, Status: i.Status,
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// ListImages returns what an org may launch: shared distributions + 1-click
// applications, plus the org's OWN custom images.
func ListImages(org string) ([]ImageInfo, error) {
	hc, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	cli := hc.Client
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opt := &godo.ListOptions{Page: 1, PerPage: 200}

	out := []ImageInfo{}
	if dist, _, err := cli.Images.ListDistribution(ctx, opt); err == nil {
		for _, i := range dist {
			out = append(out, imageInfo(i, "distribution"))
		}
	}
	if apps, _, err := cli.Images.ListApplication(ctx, opt); err == nil {
		for _, i := range apps {
			out = append(out, imageInfo(i, "application"))
		}
	}
	// Org-scoped custom images: user images carrying this org's tag.
	if user, _, err := cli.Images.ListUser(ctx, opt); err == nil {
		tag := orgTag(org)
		for _, i := range user {
			if hasTag(i.Tags, tag) {
				out = append(out, imageInfo(i, "custom"))
			}
		}
	}
	return out, nil
}

// CreateOrgImage registers a custom image from a URL into the house account,
// tagged to org so only that org sees it in ListImages. Creation is async
// (Status "pending" -> "available"); once available the image is launchable by
// its returned ID (LaunchOrgMachine accepts a numeric ImageID as a custom image).
func CreateOrgImage(org, name, url, region, distribution string) (*ImageInfo, error) {
	if name == "" || url == "" || region == "" {
		return nil, fmt.Errorf("name, url and region are required")
	}
	hc, err := newHouseDOClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	img, _, err := hc.Client.Images.Create(ctx, &godo.CustomImageCreateRequest{
		Name:         name,
		Url:          url,
		Region:       region,
		Distribution: distribution,
		Tags:         []string{orgTag(org)},
	})
	if err != nil {
		return nil, fmt.Errorf("create custom image: %w", err)
	}
	info := imageInfo(*img, "custom")
	return &info, nil
}
