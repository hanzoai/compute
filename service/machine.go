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

package service

import "fmt"

// CreateMachineSpec describes parameters for launching a new cloud instance.
type CreateMachineSpec struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"displayName"`
	InstanceType string            `json:"instanceType"` // e.g. "t3.medium", "mac2.metal"
	ImageID      string            `json:"imageId"`      // AMI ID, image name, etc.
	OS           string            `json:"os"`           // "linux", "macos", "windows"
	Region       string            `json:"region"`
	Tags         map[string]string `json:"tags,omitempty"`
	SSHKeyIDs    []string          `json:"sshKeyIds,omitempty"` // Provider SSH key IDs
}

type MachineClientInterface interface {
	GetMachines() ([]*Machine, error)
	GetMachine(name string) (*Machine, error)
	UpdateMachineState(name string, state string) (bool, string, error)
	CreateMachine(spec *CreateMachineSpec) (*Machine, error)
}

func NewMachineClient(providerType string, accessKeyId string, accessKeySecret string, region string) (MachineClientInterface, error) {
	var res MachineClientInterface
	var err error
	if providerType == "Aliyun" {
		res, err = newMachineAliyunClient(accessKeyId, accessKeySecret, region)
	} else if providerType == "Azure" {
		res, err = newMachineAzureClient(accessKeyId, accessKeySecret)
	} else if providerType == "VMware" {
		res, err = newMachineVmwareClient(accessKeyId, accessKeySecret)
	} else if providerType == "KVM" {
		res, err = newMachineKvmClient(accessKeyId, accessKeySecret)
	} else if providerType == "PVE" {
		res, err = newMachinePveClient(accessKeyId, accessKeySecret)
	} else if providerType == "Google Cloud" {
		res, err = newMachineGcpClient(accessKeyId, accessKeySecret, region)
	} else if providerType == "AWS" {
		res, err = newMachineAwsClient(accessKeyId, accessKeySecret, region)
	} else if providerType == "DigitalOcean" {
		res, err = newMachineDigitalOceanClient(accessKeyId, accessKeySecret, region)
	} else if providerType == "AWS Lightsail" {
		res, err = newMachineLightsailClient(accessKeyId, accessKeySecret, region)
	} else if providerType == "Hetzner" {
		res, err = newMachineHetznerClient(accessKeyId, accessKeySecret, region)
	} else {
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}

	if err != nil {
		return nil, err
	}

	return res, nil
}
