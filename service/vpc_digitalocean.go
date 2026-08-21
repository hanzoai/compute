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

package service

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

type VpcDigitalOceanClient struct {
	Client *godo.Client
	region string
}

func newVpcDigitalOceanClient(accessKeyId string, accessKeySecret string, region string) (*VpcDigitalOceanClient, error) {
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}
	return &VpcDigitalOceanClient{Client: newDOClient(token, nil), region: region}, nil
}

// vpcStateActive — a DigitalOcean VPC has no lifecycle field. It is created
// synchronously and has no pending or errored state, so a VPC the API returns
// exists and is usable. "active" is the accurate model rather than an invented
// value read off a field that does not exist.
const vpcStateActive = "active"

func getVpcFromDOVpc(v *godo.VPC) *Vpc {
	return &Vpc{
		Name:        v.ID,
		Id:          v.ID,
		DisplayName: v.Name,
		Region:      v.RegionSlug,
		Cidr:        v.IPRange,
		State:       vpcStateActive,
	}
}

// GetVpcs walks every page. A single List answers at most one page, so returning
// it directly would silently report a fraction of the account as the whole of it.
func (c *VpcDigitalOceanClient) GetVpcs() ([]*Vpc, error) {
	var result []*Vpc
	opt := &godo.ListOptions{PerPage: 200}
	for {
		vpcs, resp, err := c.Client.VPCs.List(context.TODO(), opt)
		if err != nil {
			return nil, err
		}
		for _, v := range vpcs {
			if c.region != "" && v.RegionSlug != c.region {
				continue
			}
			result = append(result, getVpcFromDOVpc(v))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return result, nil
		}
		page, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opt.Page = page + 1
	}
}

func (c *VpcDigitalOceanClient) GetVpc(name string) (*Vpc, error) {
	v, _, err := c.Client.VPCs.Get(context.TODO(), name)
	if err != nil {
		return nil, err
	}
	return getVpcFromDOVpc(v), nil
}

func (c *VpcDigitalOceanClient) CreateVpc(spec *CreateVpcSpec) (*Vpc, error) {
	region := spec.Region
	if region == "" {
		region = c.region
	}
	if region == "" {
		return nil, fmt.Errorf("vpc region is required")
	}

	req := &godo.VPCCreateRequest{
		Name:       spec.DisplayName,
		RegionSlug: region,
		IPRange:    spec.Cidr, // empty lets DigitalOcean allocate the range
	}

	v, _, err := c.Client.VPCs.Create(context.TODO(), req)
	if err != nil {
		return nil, fmt.Errorf("failed to create DO vpc: %w", err)
	}
	return getVpcFromDOVpc(v), nil
}

func (c *VpcDigitalOceanClient) DeleteVpc(name string) error {
	_, err := c.Client.VPCs.Delete(context.TODO(), name)
	return err
}
