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

func NewMachineClient(c Credential) (MachineClientInterface, error) {
	hc, err := httpFor(c)
	if err != nil {
		return nil, err
	}
	id, secret, region := c.KeyID, c.Secret, c.Region

	switch c.Provider {
	// Clouds whose SDK takes our http.Client, so the call can be carried by
	// egress and this process need never hold the key.
	case "DigitalOcean":
		return newMachineDigitalOceanClient(secret, id, region, hc)
	case "Hetzner":
		return newMachineHetznerClient(secret, id, region, hc)
	}

	// Every other cloud builds its own transport, so it would authenticate from
	// a token held HERE. Under a carrier that is exactly the thing being removed,
	// so it is refused rather than quietly bypassing egress — a credential that
	// escapes the door is worse than a cloud that is briefly unavailable.
	if carrierRegistered() {
		return nil, fmt.Errorf("provider %s cannot route through the carrier yet: it would hold the credential directly", c.Provider)
	}

	var res MachineClientInterface
	switch c.Provider {
	case "Aliyun":
		res, err = newMachineAliyunClient(id, secret, region)
	case "Azure":
		res, err = newMachineAzureClient(id, secret)
	case "VMware":
		res, err = newMachineVmwareClient(id, secret)
	case "KVM":
		res, err = newMachineKvmClient(id, secret)
	case "PVE":
		res, err = newMachinePveClient(id, secret)
	case "Google Cloud":
		res, err = newMachineGcpClient(id, secret, region)
	case "AWS":
		res, err = newMachineAwsClient(id, secret, region)
	case "AWS Lightsail":
		res, err = newMachineLightsailClient(id, secret, region)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", c.Provider)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}
