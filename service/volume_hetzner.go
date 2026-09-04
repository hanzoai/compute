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
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

type VolumeHetznerClient struct {
	Client *hcloud.Client
	region string
}

func newVolumeHetznerClient(accessKeyId string, accessKeySecret string, region string) (*VolumeHetznerClient, error) {
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}
	client := hcloud.NewClient(hcloud.WithToken(token))
	return &VolumeHetznerClient{Client: client, region: region}, nil
}

func getVolumeFromHetzner(vol *hcloud.Volume) *Volume {
	v := &Volume{
		Name:        strconv.FormatInt(vol.ID, 10),
		Id:          strconv.FormatInt(vol.ID, 10),
		DisplayName: vol.Name,
		Size:        vol.Size,
		Format:      vol.LinuxDevice,
	}

	if vol.Location != nil {
		v.Region = vol.Location.Name
	}

	if vol.Server != nil {
		v.MachineName = strconv.FormatInt(vol.Server.ID, 10)
		v.State = "Attached"
	} else {
		v.State = "Available"
	}

	switch vol.Status {
	case hcloud.VolumeStatusAvailable:
		if v.State != "Attached" {
			v.State = "Available"
		}
	case hcloud.VolumeStatusCreating:
		v.State = "Creating"
	}

	return v
}

func (c *VolumeHetznerClient) GetVolumes() ([]*Volume, error) {
	vols, err := c.Client.Volume.All(context.TODO())
	if err != nil {
		return nil, err
	}

	var result []*Volume
	for _, vol := range vols {
		if c.region != "" && vol.Location != nil && vol.Location.Name != c.region {
			continue
		}
		result = append(result, getVolumeFromHetzner(vol))
	}
	return result, nil
}

func (c *VolumeHetznerClient) GetVolume(name string) (*Volume, error) {
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid Hetzner volume ID: %s", name)
	}

	vol, _, err := c.Client.Volume.GetByID(context.TODO(), id)
	if err != nil {
		return nil, err
	}
	if vol == nil {
		return nil, fmt.Errorf("Hetzner volume not found: %s", name)
	}

	return getVolumeFromHetzner(vol), nil
}

func (c *VolumeHetznerClient) CreateVolume(spec *CreateVolumeSpec) (*Volume, error) {
	if spec.Size <= 0 {
		return nil, fmt.Errorf("volume size must be positive")
	}

	location := spec.Region
	if location == "" {
		location = c.region
	}
	if location == "" {
		location = "fsn1"
	}

	format := spec.Format
	if format == "" {
		format = "ext4"
	}

	opts := hcloud.VolumeCreateOpts{
		Name:     spec.DisplayName,
		Size:     spec.Size,
		Location: &hcloud.Location{Name: location},
		Format:   hcloud.Ptr(format),
		Labels: map[string]string{
			"managed-by": managedBy,
		},
	}

	if spec.MachineID != "" {
		serverID, err := strconv.ParseInt(spec.MachineID, 10, 64)
		if err == nil {
			opts.Server = &hcloud.Server{ID: serverID}
		}
	}

	result, _, err := c.Client.Volume.Create(context.TODO(), opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create Hetzner volume: %w", err)
	}

	return getVolumeFromHetzner(result.Volume), nil
}

func (c *VolumeHetznerClient) DeleteVolume(name string) error {
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Hetzner volume ID: %s", name)
	}

	_, err = c.Client.Volume.Delete(context.TODO(), &hcloud.Volume{ID: id})
	return err
}

func (c *VolumeHetznerClient) AttachVolume(volumeName string, machineName string) error {
	volID, err := strconv.ParseInt(volumeName, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid volume ID: %s", volumeName)
	}

	serverID, err := strconv.ParseInt(machineName, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid server ID: %s", machineName)
	}

	_, _, err = c.Client.Volume.Attach(context.TODO(),
		&hcloud.Volume{ID: volID},
		&hcloud.Server{ID: serverID},
	)
	return err
}

func (c *VolumeHetznerClient) DetachVolume(volumeName string) error {
	volID, err := strconv.ParseInt(volumeName, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid volume ID: %s", volumeName)
	}

	_, _, err = c.Client.Volume.Detach(context.TODO(), &hcloud.Volume{ID: volID})
	return err
}

func (c *VolumeHetznerClient) ResizeVolume(volumeName string, sizeGB int) error {
	volID, err := strconv.ParseInt(volumeName, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid volume ID: %s", volumeName)
	}

	_, _, err = c.Client.Volume.Resize(context.TODO(), &hcloud.Volume{ID: volID}, sizeGB)
	return err
}
