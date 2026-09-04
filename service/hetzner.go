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
	"net/http"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

type MachineHetznerClient struct {
	Client *hcloud.Client
	region string
}

// Volumes is Hetzner's volume noun over the same hcloud client. Hetzner sells no
// managed Kubernetes, VPC or load balancer through this service, so it implements
// none of those capabilities and the factories say so without a list.
func (c MachineHetznerClient) Volumes() VolumeClientInterface {
	return &VolumeHetznerClient{Client: c.Client, region: c.region}
}

func newMachineHetznerClient(accessKeySecret string, accessKeyId string, region string, hc *http.Client) (MachineHetznerClient, error) {
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}

	// Options in order: the carried transport, then the token only if we hold
	// one. Under a carrier the token is empty and egress attaches it.
	opts := []hcloud.ClientOption{}
	if hc != nil {
		opts = append(opts, hcloud.WithHTTPClient(hc))
	}
	if token != "" {
		opts = append(opts, hcloud.WithToken(token))
	}
	return MachineHetznerClient{Client: hcloud.NewClient(opts...), region: region}, nil
}

func getMachineFromHetznerServer(server *hcloud.Server) *Machine {
	machine := &Machine{
		Name:        strconv.FormatInt(server.ID, 10),
		Id:          strconv.FormatInt(server.ID, 10),
		DisplayName: server.Name,
		State:       "Unknown",
	}

	if server.Datacenter != nil {
		machine.Region = server.Datacenter.Name
	}

	if server.ServerType != nil {
		machine.Size = server.ServerType.Name
		machine.CpuSize = strconv.Itoa(int(server.ServerType.Cores))
		machine.MemSize = fmt.Sprintf("%.0f", server.ServerType.Memory)
	}

	if server.Image != nil {
		machine.Image = server.Image.Name
		if server.Image.OSFlavor != "" {
			machine.Os = fmt.Sprintf("%s %s", server.Image.OSFlavor, server.Image.OSVersion)
		} else {
			machine.Os = server.Image.Description
		}
	}

	if !server.PublicNet.IPv4.IP.IsUnspecified() {
		machine.PublicIp = server.PublicNet.IPv4.IP.String()
	}

	if len(server.PrivateNet) > 0 {
		machine.PrivateIp = server.PrivateNet[0].IP.String()
	}

	switch server.Status {
	case hcloud.ServerStatusRunning:
		machine.State = "Running"
	case hcloud.ServerStatusOff:
		machine.State = "Stopped"
	case hcloud.ServerStatusInitializing:
		machine.State = "Starting"
	case hcloud.ServerStatusStarting:
		machine.State = "Starting"
	case hcloud.ServerStatusStopping:
		machine.State = "Stopping"
	default:
		machine.State = string(server.Status)
	}

	for k, v := range server.Labels {
		machine.Tag += fmt.Sprintf("%s=%s,", k, v)
	}

	return machine
}

func (client MachineHetznerClient) GetMachines() ([]*Machine, error) {
	servers, err := client.Client.Server.All(context.TODO())
	if err != nil {
		return nil, err
	}

	var allMachines []*Machine
	for _, server := range servers {
		if client.region != "" && server.Datacenter != nil && server.Datacenter.Location != nil {
			if server.Datacenter.Location.Name != client.region {
				continue
			}
		}
		allMachines = append(allMachines, getMachineFromHetznerServer(server))
	}

	return allMachines, nil
}

func (client MachineHetznerClient) GetMachine(name string) (*Machine, error) {
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid Hetzner server ID: %s", name)
	}

	server, _, err := client.Client.Server.GetByID(context.TODO(), id)
	if err != nil {
		return nil, err
	}
	if server == nil {
		return nil, fmt.Errorf("Hetzner server not found: %s", name)
	}

	return getMachineFromHetznerServer(server), nil
}

func (client MachineHetznerClient) UpdateMachineState(name string, state string) (bool, string, error) {
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return false, "", fmt.Errorf("invalid Hetzner server ID: %s", name)
	}

	server := &hcloud.Server{ID: id}

	switch state {
	case "Running":
		_, _, err = client.Client.Server.Poweron(context.TODO(), server)
		if err != nil {
			return false, "", err
		}
	case "Stopped":
		_, _, err = client.Client.Server.Shutdown(context.TODO(), server)
		if err != nil {
			return false, "", err
		}
	case "Rebooting":
		_, _, err = client.Client.Server.Reboot(context.TODO(), server)
		if err != nil {
			return false, "", err
		}
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}

	return true, fmt.Sprintf("Hetzner server: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}

func (client MachineHetznerClient) CreateMachine(spec *CreateMachineSpec) (*Machine, error) {
	if spec.ImageID == "" {
		return nil, fmt.Errorf("imageId is required for Hetzner server creation")
	}

	serverType := spec.InstanceType
	if serverType == "" {
		serverType = "cx22"
	}

	location := spec.Region
	if location == "" {
		location = client.region
	}
	if location == "" {
		location = "fsn1"
	}

	labels := map[string]string{
		"managed-by": managedBy,
	}
	if spec.OS != "" {
		labels["os"] = spec.OS
	}
	for k, v := range spec.Tags {
		labels[k] = v
	}

	opts := hcloud.ServerCreateOpts{
		Name:       spec.DisplayName,
		ServerType: &hcloud.ServerType{Name: serverType},
		Image:      &hcloud.Image{Name: spec.ImageID},
		Location:   &hcloud.Location{Name: location},
		Labels:     labels,
	}

	result, _, err := client.Client.Server.Create(context.TODO(), opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create Hetzner server: %w", err)
	}

	return getMachineFromHetznerServer(result.Server), nil
}
