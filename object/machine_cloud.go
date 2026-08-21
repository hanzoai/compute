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
	"context"
	"fmt"

	"github.com/hanzoai/visor/service"
)

// GetKubernetesNodesCloud returns DOKS worker nodes — as service.Machines — for
// every active BYOC DigitalOcean provider that names a cluster (Provider.ClusterID).
// This is the Provider-record cluster→org association; the platform-account tag
// association lives in service.ListOrgKubernetesNodes and the controller unions
// both, so a cluster discovered by either path surfaces its nodes (deduped by
// droplet id) exactly once. The DO provider selection mirrors SyncNodePoolsCloud
// (Type=="DigitalOcean" && ClusterID!=""), so nodes come from the same clusters
// visor already reconciles pools for.
func GetKubernetesNodesCloud(owner string) ([]*service.Machine, error) {
	providers, err := getActiveCloudProviders(owner)
	if err != nil {
		return nil, err
	}
	var machines []*service.Machine
	for _, provider := range providers {
		if provider.Type != "DigitalOcean" || provider.ClusterID == "" {
			continue
		}
		token := provider.ClientSecret
		if token == "" {
			token = provider.ClientId
		}
		client, err := service.NewDOKSClient(token, provider.ClusterID)
		if err != nil {
			return nil, err
		}
		nodes, err := client.NodeMachines(context.Background())
		if err != nil {
			return nil, err
		}
		machines = append(machines, nodes...)
	}
	return machines, nil
}

func getMachineFromService(owner string, provider string, clientMachine *service.Machine) *Machine {
	return &Machine{
		Owner:       owner,
		Name:        clientMachine.Name,
		Id:          clientMachine.Id,
		Provider:    provider,
		CreatedTime: clientMachine.CreatedTime,
		UpdatedTime: clientMachine.UpdatedTime,
		ExpireTime:  clientMachine.ExpireTime,
		DisplayName: clientMachine.DisplayName,
		Region:      clientMachine.Region,
		Zone:        clientMachine.Zone,
		Category:    clientMachine.Category,
		Type:        clientMachine.Type,
		Size:        clientMachine.Size,
		Tag:         clientMachine.Tag,
		State:       clientMachine.State,
		Image:       clientMachine.Image,
		Os:          clientMachine.Os,
		PublicIp:    clientMachine.PublicIp,
		PrivateIp:   clientMachine.PrivateIp,
		CpuSize:     clientMachine.CpuSize,
		MemSize:     clientMachine.MemSize,
	}
}

func getMachinesCloud(owner string) ([]*Machine, error) {
	machines := []*Machine{}
	providers, err := getActiveCloudProviders(owner)
	if err != nil {
		return nil, err
	}

	for _, provider := range providers {
		// A launch cycles across a provider's accounts, so a machine can live on
		// any one of them. List every account — on most clouds a resource under
		// one account's key is invisible to another's, so listing only the
		// provider's own account would silently drop the rest from tracking.
		for _, cred := range provider.LaunchCredentials() {
			client, err2 := service.NewMachineClient(provider.credential(cred))
			if err2 != nil {
				return nil, err2
			}

			clientMachines, err2 := client.GetMachines()
			if err2 != nil {
				if provider.Type != "VMware" {
					return nil, err2
				}
			}

			for _, clientMachine := range clientMachines {
				machine := getMachineFromService(owner, provider.Name, clientMachine)
				machine.Account = cred.KeyName
				machines = append(machines, machine)
			}
		}
	}

	return machines, nil
}

func SyncMachinesCloud(owner string) (bool, error) {
	machines, err := getMachinesCloud(owner)
	if err != nil {
		return false, err
	}

	dbMachines, err := GetMachines(owner)
	if err != nil {
		return false, err
	}

	dbMachineMap := map[string]*Machine{}
	for _, dbMachine := range dbMachines {
		dbMachineMap[dbMachine.GetId()] = dbMachine
	}

	for _, machine := range machines {
		if dbMachine, ok := dbMachineMap[machine.GetId()]; ok {
			machine.RemoteProtocol = dbMachine.RemoteProtocol
			machine.RemotePort = dbMachine.RemotePort
			machine.RemoteUsername = dbMachine.RemoteUsername
			machine.RemotePassword = dbMachine.RemotePassword
		}
	}

	_, err = deleteMachines(owner)
	if err != nil {
		return false, err
	}

	if len(machines) == 0 {
		return false, nil
	}

	affected, err := addMachines(owner, machines)
	return affected, err
}

// CreateMachineCloud launches a new VM via the cloud provider and registers it in the DB.
func CreateMachineCloud(owner string, providerName string, spec *service.CreateMachineSpec) (*Machine, error) {
	provider, err := getProvider(owner, providerName)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("provider %q not found for owner %q", providerName, owner)
	}

	cred, ok := provider.LaunchCredentialFor()
	if !ok {
		return nil, fmt.Errorf("provider %q for owner %q has no usable account to launch on", providerName, owner)
	}

	client, err := service.NewMachineClient(provider.credential(cred))
	if err != nil {
		return nil, fmt.Errorf("failed to create machine client: %w", err)
	}

	clientMachine, err := client.CreateMachine(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud machine: %w", err)
	}

	machine := getMachineFromService(owner, providerName, clientMachine)
	machine.Account = cred.KeyName
	machine.Os = spec.OS

	// Set remote access defaults based on OS
	switch spec.OS {
	case "windows":
		machine.RemoteProtocol = "RDP"
		machine.RemotePort = 3389
	case "macos":
		machine.RemoteProtocol = "VNC"
		machine.RemotePort = 5900
	default:
		machine.RemoteProtocol = "SSH"
		machine.RemotePort = 22
	}

	// Persist to DB
	_, err = AddMachine(machine)
	if err != nil {
		return nil, fmt.Errorf("machine created in cloud but DB insert failed: %w", err)
	}

	return machine, nil
}

func updateMachineCloud(oldMachine *Machine, machine *Machine) (bool, error) {
	provider, err := getProvider(oldMachine.Owner, oldMachine.Provider)
	if err != nil {
		return false, err
	}
	if provider == nil {
		return false, fmt.Errorf("The provider: %s does not exist", machine.Provider)
	}

	cred, ok := provider.launchCredentialNamed(oldMachine.Account)
	if !ok {
		return false, fmt.Errorf("provider %q account %q for machine %q is no longer usable", oldMachine.Provider, oldMachine.Account, oldMachine.Name)
	}

	client, err := service.NewMachineClient(provider.credential(cred))
	if err != nil {
		return false, err
	}

	if oldMachine.State != machine.State {
		affected, _, err := client.UpdateMachineState(oldMachine.Name, machine.State)
		if err != nil {
			return false, err
		}

		return affected, nil
	}

	return false, nil
}
