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

import (
	"context"
	"fmt"
	"strconv"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

type MachineDigitalOceanClient struct {
	Client *godo.Client
	region string
}

func newMachineDigitalOceanClient(accessKeyId string, accessKeySecret string, region string) (MachineDigitalOceanClient, error) {
	// DigitalOcean uses a single API token (passed as accessKeySecret).
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)
	client := godo.NewClient(oauthClient)

	return MachineDigitalOceanClient{Client: client, region: region}, nil
}

func getMachineFromDroplet(droplet godo.Droplet) *Machine {
	machine := &Machine{
		Name:        strconv.Itoa(droplet.ID),
		Id:          strconv.Itoa(droplet.ID),
		DisplayName: droplet.Name,
		Region:      droplet.Region.Slug,
		Size:        droplet.Size.Slug,
		Image:       droplet.Image.Slug,
		Os:          fmt.Sprintf("%s %s", droplet.Image.Distribution, droplet.Image.Name),
		CpuSize:     strconv.Itoa(droplet.Vcpus),
		MemSize:     strconv.Itoa(droplet.Memory),
	}

	switch droplet.Status {
	case "active":
		machine.State = "Running"
	case "off":
		machine.State = "Stopped"
	case "new":
		machine.State = "Starting"
	default:
		machine.State = droplet.Status
	}

	if v4 := droplet.Networks.V4; len(v4) > 0 {
		for _, net := range v4 {
			if net.Type == "public" {
				machine.PublicIp = net.IPAddress
			} else if net.Type == "private" {
				machine.PrivateIp = net.IPAddress
			}
		}
	}

	for _, tag := range droplet.Tags {
		machine.Tag += tag + ","
	}

	return machine
}

func (client MachineDigitalOceanClient) GetMachines() ([]*Machine, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	var allMachines []*Machine

	for {
		droplets, resp, err := client.Client.Droplets.List(context.TODO(), opt)
		if err != nil {
			return nil, err
		}

		for _, d := range droplets {
			if client.region != "" && d.Region.Slug != client.region {
				continue
			}
			allMachines = append(allMachines, getMachineFromDroplet(d))
		}

		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opt.Page++
	}

	return allMachines, nil
}

func (client MachineDigitalOceanClient) GetMachine(name string) (*Machine, error) {
	id, err := strconv.Atoi(name)
	if err != nil {
		return nil, fmt.Errorf("invalid droplet ID: %s", name)
	}

	droplet, _, err := client.Client.Droplets.Get(context.TODO(), id)
	if err != nil {
		return nil, err
	}

	return getMachineFromDroplet(*droplet), nil
}

func (client MachineDigitalOceanClient) UpdateMachineState(name string, state string) (bool, string, error) {
	id, err := strconv.Atoi(name)
	if err != nil {
		return false, "", fmt.Errorf("invalid droplet ID: %s", name)
	}

	switch state {
	case "Running":
		_, _, err = client.Client.DropletActions.PowerOn(context.TODO(), id)
		if err != nil {
			return false, "", err
		}
	case "Stopped":
		_, _, err = client.Client.DropletActions.Shutdown(context.TODO(), id)
		if err != nil {
			return false, "", err
		}
	case "Rebooting":
		_, _, err = client.Client.DropletActions.Reboot(context.TODO(), id)
		if err != nil {
			return false, "", err
		}
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}

	return true, fmt.Sprintf("Droplet: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}

func (client MachineDigitalOceanClient) CreateMachine(spec *CreateMachineSpec) (*Machine, error) {
	return nil, fmt.Errorf("CreateMachine not yet implemented for DigitalOcean")
}
