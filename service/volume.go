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

type Volume struct {
	Name        string
	Id          string
	DisplayName string
	Region      string
	Size        int    // GB
	State       string // "Available", "Attached", "Creating"
	Format      string
	MachineName string // attached server name/ID, empty if detached
}

type CreateVolumeSpec struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Size        int    `json:"size"`       // GB
	Region      string `json:"region"`
	Format      string `json:"format"`     // "ext4", "xfs"
	MachineID   string `json:"machineId"`  // optional: attach on create
}

type VolumeClientInterface interface {
	GetVolumes() ([]*Volume, error)
	GetVolume(name string) (*Volume, error)
	CreateVolume(spec *CreateVolumeSpec) (*Volume, error)
	DeleteVolume(name string) error
	AttachVolume(volumeName string, machineName string) error
	DetachVolume(volumeName string) error
	ResizeVolume(volumeName string, sizeGB int) error
}

func NewVolumeClient(providerType string, accessKeyId string, accessKeySecret string, region string) (VolumeClientInterface, error) {
	if providerType == "Hetzner" {
		return newVolumeHetznerClient(accessKeyId, accessKeySecret, region)
	} else if providerType == "DigitalOcean" {
		return newVolumeDigitalOceanClient(accessKeyId, accessKeySecret, region)
	}
	return nil, fmt.Errorf("volume support not available for provider type: %s", providerType)
}
