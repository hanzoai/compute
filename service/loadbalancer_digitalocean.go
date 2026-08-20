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

type LoadBalancerDigitalOceanClient struct {
	Client *godo.Client
	region string
}

func newLoadBalancerDigitalOceanClient(accessKeyId string, accessKeySecret string, region string) (*LoadBalancerDigitalOceanClient, error) {
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}
	return &LoadBalancerDigitalOceanClient{Client: newDOClient(token), region: region}, nil
}

func getLoadBalancerFromDOLoadBalancer(lb *godo.LoadBalancer) *LoadBalancer {
	region := ""
	if lb.Region != nil {
		region = lb.Region.Slug
	}
	return &LoadBalancer{
		Name:        lb.ID,
		Id:          lb.ID,
		DisplayName: lb.Name,
		Region:      region,
		Type:        lb.Type,
		Ip:          lb.IP,
		State:       lb.Status,
		// Targets is the count of backends actually attached, which is the
		// number an operator can act on — not a configured capacity.
		Targets: len(lb.DropletIDs),
	}
}

// GetLoadBalancers walks every page, for the reason GetVpcs does.
func (c *LoadBalancerDigitalOceanClient) GetLoadBalancers() ([]*LoadBalancer, error) {
	var result []*LoadBalancer
	opt := &godo.ListOptions{PerPage: 200}
	for {
		lbs, resp, err := c.Client.LoadBalancers.List(context.TODO(), opt)
		if err != nil {
			return nil, err
		}
		for i := range lbs {
			lb := getLoadBalancerFromDOLoadBalancer(&lbs[i])
			if c.region != "" && lb.Region != c.region {
				continue
			}
			result = append(result, lb)
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

func (c *LoadBalancerDigitalOceanClient) GetLoadBalancer(name string) (*LoadBalancer, error) {
	lb, _, err := c.Client.LoadBalancers.Get(context.TODO(), name)
	if err != nil {
		return nil, err
	}
	return getLoadBalancerFromDOLoadBalancer(lb), nil
}

func (c *LoadBalancerDigitalOceanClient) CreateLoadBalancer(spec *CreateLoadBalancerSpec) (*LoadBalancer, error) {
	region := spec.Region
	if region == "" {
		region = c.region
	}
	if region == "" {
		return nil, fmt.Errorf("load balancer region is required")
	}

	req := &godo.LoadBalancerRequest{
		Name:            spec.DisplayName,
		Region:          region,
		Type:            spec.Type, // empty takes the provider default
		SizeSlug:        spec.Size, // empty takes the provider default
		ForwardingRules: toDOForwardingRules(spec.ForwardingRules),
	}

	lb, _, err := c.Client.LoadBalancers.Create(context.TODO(), req)
	if err != nil {
		return nil, fmt.Errorf("failed to create DO load balancer: %w", err)
	}
	return getLoadBalancerFromDOLoadBalancer(lb), nil
}

func (c *LoadBalancerDigitalOceanClient) DeleteLoadBalancer(name string) error {
	_, err := c.Client.LoadBalancers.Delete(context.TODO(), name)
	return err
}

// toDOForwardingRules maps our rules onto the provider's. An empty list becomes
// plain HTTP 80 to 80 rather than nothing: the provider rejects a load balancer
// with no rules, so the caller who omitted them would get an error instead of
// the obvious balancer they asked for.
func toDOForwardingRules(rules []ForwardingRule) []godo.ForwardingRule {
	if len(rules) == 0 {
		return []godo.ForwardingRule{{
			EntryProtocol:  "http",
			EntryPort:      80,
			TargetProtocol: "http",
			TargetPort:     80,
		}}
	}
	out := make([]godo.ForwardingRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, godo.ForwardingRule{
			EntryProtocol:  r.EntryProtocol,
			EntryPort:      r.EntryPort,
			TargetProtocol: r.TargetProtocol,
			TargetPort:     r.TargetPort,
		})
	}
	return out
}
