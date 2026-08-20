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

// Vpc is a private network, in the shape every provider is mapped onto.
type Vpc struct {
	Name        string
	Id          string
	DisplayName string
	Region      string
	Cidr        string
	State       string
}

type CreateVpcSpec struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Region      string `json:"region"`
	Cidr        string `json:"cidr"`
}

type VpcClientInterface interface {
	GetVpcs() ([]*Vpc, error)
	GetVpc(name string) (*Vpc, error)
	CreateVpc(spec *CreateVpcSpec) (*Vpc, error)
	DeleteVpc(name string) error
}

func NewVpcClient(providerType string, accessKeyId string, accessKeySecret string, region string) (VpcClientInterface, error) {
	if providerType == "DigitalOcean" {
		return newVpcDigitalOceanClient(accessKeyId, accessKeySecret, region)
	}
	return nil, fmt.Errorf("vpc support not available for provider type: %s", providerType)
}
