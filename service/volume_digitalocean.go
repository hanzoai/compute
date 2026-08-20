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

	"github.com/digitalocean/godo"
)

type VolumeDigitalOceanClient struct {
	Client *godo.Client
	region string
}

func newVolumeDigitalOceanClient(accessKeyId string, accessKeySecret string, region string) (*VolumeDigitalOceanClient, error) {
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}
	return &VolumeDigitalOceanClient{Client: newDOClient(token), region: region}, nil
}

func getVolumeFromDOVolume(vol *godo.Volume) *Volume {
	v := &Volume{
		Name:        vol.ID,
		Id:          vol.ID,
		DisplayName: vol.Name,
		Region:      vol.Region.Slug,
		Size:        int(vol.SizeGigaBytes),
		Format:      vol.FilesystemType,
	}

	if len(vol.DropletIDs) > 0 {
		v.MachineName = strconv.Itoa(vol.DropletIDs[0])
		v.State = "Attached"
	} else {
		v.State = "Available"
	}

	return v
}

func (c *VolumeDigitalOceanClient) GetVolumes() ([]*Volume, error) {
	params := &godo.ListVolumeParams{}
	if c.region != "" {
		params.Region = c.region
	}

	vols, _, err := c.Client.Storage.ListVolumes(context.TODO(), params)
	if err != nil {
		return nil, err
	}

	var result []*Volume
	for i := range vols {
		result = append(result, getVolumeFromDOVolume(&vols[i]))
	}
	return result, nil
}

func (c *VolumeDigitalOceanClient) GetVolume(name string) (*Volume, error) {
	vol, _, err := c.Client.Storage.GetVolume(context.TODO(), name)
	if err != nil {
		return nil, err
	}
	return getVolumeFromDOVolume(vol), nil
}

func (c *VolumeDigitalOceanClient) CreateVolume(spec *CreateVolumeSpec) (*Volume, error) {
	if spec.Size <= 0 {
		return nil, fmt.Errorf("volume size must be positive")
	}

	region := spec.Region
	if region == "" {
		region = c.region
	}
	if region == "" {
		region = "nyc1"
	}

	format := spec.Format
	if format == "" {
		format = "ext4"
	}

	req := &godo.VolumeCreateRequest{
		Name:            spec.DisplayName,
		Region:          region,
		SizeGigaBytes:   int64(spec.Size),
		FilesystemType:  format,
		FilesystemLabel: spec.Name,
	}

	vol, _, err := c.Client.Storage.CreateVolume(context.TODO(), req)
	if err != nil {
		return nil, fmt.Errorf("failed to create DO volume: %w", err)
	}

	return getVolumeFromDOVolume(vol), nil
}

func (c *VolumeDigitalOceanClient) DeleteVolume(name string) error {
	_, err := c.Client.Storage.DeleteVolume(context.TODO(), name)
	return err
}

func (c *VolumeDigitalOceanClient) AttachVolume(volumeName string, machineName string) error {
	dropletID, err := strconv.Atoi(machineName)
	if err != nil {
		return fmt.Errorf("invalid droplet ID: %s", machineName)
	}

	_, _, err = c.Client.StorageActions.Attach(context.TODO(), volumeName, dropletID)
	return err
}

func (c *VolumeDigitalOceanClient) DetachVolume(volumeName string) error {
	// DO requires droplet ID to detach; detach from all by getting volume first
	vol, _, err := c.Client.Storage.GetVolume(context.TODO(), volumeName)
	if err != nil {
		return err
	}
	if len(vol.DropletIDs) == 0 {
		return nil // already detached
	}

	_, _, err = c.Client.StorageActions.DetachByDropletID(context.TODO(), volumeName, vol.DropletIDs[0])
	return err
}

func (c *VolumeDigitalOceanClient) ResizeVolume(volumeName string, sizeGB int) error {
	vol, _, err := c.Client.Storage.GetVolume(context.TODO(), volumeName)
	if err != nil {
		return err
	}

	_, _, err = c.Client.StorageActions.Resize(context.TODO(), vol.ID, sizeGB, vol.Region.Slug)
	return err
}
