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

import "fmt"

// LoadBalancer is a managed load balancer, in the shape every provider is
// mapped onto. Targets is the real count of attached backends, not a capacity.
type LoadBalancer struct {
	Name        string
	Id          string
	DisplayName string
	Region      string
	Type        string
	Ip          string
	State       string
	Targets     int
}

// ForwardingRule is one listen-to-backend mapping.
type ForwardingRule struct {
	EntryProtocol  string `json:"entryProtocol"`
	EntryPort      int    `json:"entryPort"`
	TargetProtocol string `json:"targetProtocol"`
	TargetPort     int    `json:"targetPort"`
}

type CreateLoadBalancerSpec struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Region      string `json:"region"`
	Type        string `json:"type"`
	Size        string `json:"size"`
	// ForwardingRules empty yields plain HTTP 80 to 80 — the same default the
	// provider's own console applies, so an omitted list is a usable balancer
	// rather than a rejected request.
	ForwardingRules []ForwardingRule `json:"forwardingRules"`
}

type LoadBalancerClientInterface interface {
	GetLoadBalancers() ([]*LoadBalancer, error)
	GetLoadBalancer(name string) (*LoadBalancer, error)
	CreateLoadBalancer(spec *CreateLoadBalancerSpec) (*LoadBalancer, error)
	DeleteLoadBalancer(name string) error
}

func NewLoadBalancerClient(providerType string, accessKeyId string, accessKeySecret string, region string) (LoadBalancerClientInterface, error) {
	// ONE registry. NewMachineClient is the only place a cloud name is matched;
	// loadbalancer support is a capability of the client it returns, so a cloud
	// is never listed twice and the two lists can never disagree.
	c, err := NewMachineClient(providerType, accessKeyId, accessKeySecret, region)
	if err != nil {
		return nil, err
	}
	p, ok := c.(LoadBalancerCapable)
	if !ok {
		return nil, fmt.Errorf("loadbalancer support not available for provider type: %s", providerType)
	}
	return p.LoadBalancers(), nil
}
