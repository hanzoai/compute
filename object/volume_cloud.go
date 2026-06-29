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

package object

import (
	"fmt"

	"github.com/hanzoai/vm/service"
	"github.com/hanzoai/vm/util"
)

func getVolumeFromService(owner string, provider string, sv *service.Volume) *Volume {
	return &Volume{
		Owner:       owner,
		Name:        sv.Name,
		Id:          sv.Id,
		Provider:    provider,
		CreatedTime: util.GetCurrentTime(),
		DisplayName: sv.DisplayName,
		Region:      sv.Region,
		Size:        sv.Size,
		State:       sv.State,
		Format:      sv.Format,
		Machine:     sv.MachineName,
	}
}

func getVolumeClient(owner string, providerName string) (service.VolumeClientInterface, error) {
	provider, err := getProvider(owner, providerName)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("provider %q not found for owner %q", providerName, owner)
	}

	client, err := service.NewVolumeClient(provider.Type, provider.ClientId, provider.ClientSecret, provider.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume client: %w", err)
	}
	return client, nil
}

func CreateVolumeCloud(owner string, providerName string, spec *service.CreateVolumeSpec) (*Volume, error) {
	client, err := getVolumeClient(owner, providerName)
	if err != nil {
		return nil, err
	}

	sv, err := client.CreateVolume(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud volume: %w", err)
	}

	volume := getVolumeFromService(owner, providerName, sv)

	_, err = AddVolume(volume)
	if err != nil {
		return nil, fmt.Errorf("volume created in cloud but DB insert failed: %w", err)
	}

	return volume, nil
}

func DeleteVolumeCloud(owner string, providerName string, volumeCloudID string) error {
	client, err := getVolumeClient(owner, providerName)
	if err != nil {
		return err
	}

	err = client.DeleteVolume(volumeCloudID)
	if err != nil {
		return fmt.Errorf("failed to delete cloud volume: %w", err)
	}

	// Remove from DB by finding matching volume
	volumes, err := GetVolumes(owner)
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if v.Id == volumeCloudID && v.Provider == providerName {
			_, _ = DeleteVolume(v)
			break
		}
	}

	return nil
}

func AttachVolumeCloud(owner string, providerName string, volumeCloudID string, machineCloudID string) error {
	client, err := getVolumeClient(owner, providerName)
	if err != nil {
		return err
	}
	return client.AttachVolume(volumeCloudID, machineCloudID)
}

func DetachVolumeCloud(owner string, providerName string, volumeCloudID string) error {
	client, err := getVolumeClient(owner, providerName)
	if err != nil {
		return err
	}
	return client.DetachVolume(volumeCloudID)
}

func ResizeVolumeCloud(owner string, providerName string, volumeCloudID string, sizeGB int) error {
	client, err := getVolumeClient(owner, providerName)
	if err != nil {
		return err
	}
	return client.ResizeVolume(volumeCloudID, sizeGB)
}
