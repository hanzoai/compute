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

// Machine is one machine as every cloud client answers it.
type Machine struct {
	Owner       string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name        string `xorm:"varchar(100) notnull pk" json:"name"`
	Id          string `xorm:"varchar(100)" json:"id"`
	Provider    string `xorm:"varchar(100)" json:"provider"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`
	ExpireTime  string `xorm:"varchar(100)" json:"expireTime"`
	DisplayName string `xorm:"varchar(100)" json:"displayName"`

	Region   string `xorm:"varchar(100)" json:"region"`
	Zone     string `xorm:"varchar(100)" json:"zone"`
	Category string `xorm:"varchar(100)" json:"category"`
	Type     string `xorm:"varchar(100)" json:"type"`
	Size     string `xorm:"varchar(100)" json:"size"`
	Tag      string `xorm:"varchar(100)" json:"tag"`
	State    string `xorm:"varchar(100)" json:"state"`

	Image     string `xorm:"varchar(100)" json:"image"`
	Os        string `xorm:"varchar(100)" json:"os"`
	PublicIp  string `xorm:"varchar(100)" json:"publicIp"`
	PrivateIp string `xorm:"varchar(100)" json:"privateIp"`
	CpuSize   string `xorm:"varchar(100)" json:"cpuSize"`
	MemSize   string `xorm:"varchar(100)" json:"memSize"`

	// instance is a hosted machine's EC2 instance id, which its metrics are
	// kept under. It never leaves service: a caller addresses a machine by Id.
	instance string
}

type MachineClientInterface interface {
	GetMachines() ([]*Machine, error)
	GetMachine(name string) (*Machine, error)
	UpdateMachineState(name string, state string) (bool, string, error)
	CreateMachine(spec *CreateMachineSpec) (*Machine, error)
}

// Credential names one cloud account a machine client is built for. It holds
// no key: the key is in egress custody, and the account is reached only through
// egress, which attaches it.
type Credential struct {
	Provider string // "Hetzner" or "AWS", as NewMachineClient spells it
	Name     string // the account's label in egress custody; "" means the only one
	Region   string
	// Tenant is the org whose OWN account this is — a bring-your-own provider
	// row — and empty for the platform's. It decides whether the account may
	// be carried: see httpFor.
	Tenant string
}

// NewMachineClient builds the client for one cloud account. Every cloud here
// takes our http.Client, so every call is carried by egress and this process
// never holds a key: Hetzner's bearer is attached there, and an AWS SDK is handed
// anonymous credentials and egress signs. A cloud whose SDK would authenticate
// from a key held in this process is not offered at all, and there is no call
// that does not go through egress (httpFor).
func NewMachineClient(c Credential) (MachineClientInterface, error) {
	hc, err := httpFor(c)
	if err != nil {
		return nil, err
	}
	switch c.Provider {
	case "Hetzner":
		return newMachineHetznerClient(c.Region, hc)
	case "AWS":
		return MachineAwsClient{Client: carriedEC2(hc, c.Region), region: c.Region}, nil
	}
	return nil, fmt.Errorf("unsupported provider type: %s", c.Provider)
}
