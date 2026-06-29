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
	region := client.region
	if spec.Region != "" {
		region = spec.Region
	}
	if region == "" {
		region = "sfo3"
	}

	size := spec.InstanceType
	if size == "" {
		size = "s-2vcpu-4gb"
	}

	image := spec.ImageID
	if image == "" {
		image = "ubuntu-24-04-x64"
	}

	tags := buildDropletTags(spec)

	var sshKeys []godo.DropletCreateSSHKey
	for _, id := range spec.SSHKeyIDs {
		if intID, err := strconv.Atoi(id); err == nil {
			sshKeys = append(sshKeys, godo.DropletCreateSSHKey{ID: intID})
		}
	}

	createReq := &godo.DropletCreateRequest{
		Name:     spec.Name,
		Region:   region,
		Size:     size,
		Image:    godo.DropletCreateImage{Slug: image},
		Tags:     tags,
		UserData: buildBotUserData(spec),
		SSHKeys:  sshKeys,
	}

	droplet, _, err := client.Client.Droplets.Create(context.TODO(), createReq)
	if err != nil {
		return nil, fmt.Errorf("create droplet: %w", err)
	}

	return getMachineFromDroplet(*droplet), nil
}

func (client MachineDigitalOceanClient) DeleteMachine(name string) error {
	id, err := strconv.Atoi(name)
	if err != nil {
		return fmt.Errorf("invalid droplet ID: %s", name)
	}

	_, err = client.Client.Droplets.Delete(context.TODO(), id)
	if err != nil {
		return fmt.Errorf("delete droplet %d: %w", id, err)
	}

	return nil
}

// buildDropletTags creates DO tags from the spec metadata.
func buildDropletTags(spec *CreateMachineSpec) []string {
	tags := []string{"managed-by:hanzo-visor"}

	if spec.DisplayName != "" {
		tags = append(tags, fmt.Sprintf("display-name:%s", spec.DisplayName))
	}
	if spec.OS != "" {
		tags = append(tags, fmt.Sprintf("os:%s", spec.OS))
	}

	for k, v := range spec.Tags {
		// Skip env: prefix tags — they are for cloud-init user data only,
		// not DigitalOcean resource tags (which also get stored in DB).
		if len(k) > 4 && k[:4] == "env:" {
			continue
		}
		tags = append(tags, fmt.Sprintf("%s:%s", k, v))
	}

	return tags
}

// buildBotUserData generates a cloud-init script that installs @hanzo/bot
// on the droplet and configures it as a systemd service connecting to the
// gateway. Environment variables are passed via spec.Tags with the
// "env:" prefix (e.g., Tags["env:BOT_NODE_GATEWAY_URL"] = "wss://gw.hanzo.bot").
func buildBotUserData(spec *CreateMachineSpec) string {
	gatewayURL := "wss://gw.hanzo.bot"
	gatewayToken := ""
	apiKey := ""
	nodeID := spec.Name
	displayName := spec.DisplayName
	if displayName == "" {
		displayName = spec.Name
	}

	// Extract env overrides from tags
	for k, v := range spec.Tags {
		switch k {
		case "env:BOT_NODE_GATEWAY_URL":
			gatewayURL = v
		case "env:BOT_GATEWAY_TOKEN":
			gatewayToken = v
		case "env:HANZO_API_KEY":
			apiKey = v
		case "env:AGENT_NODE_ID":
			nodeID = v
		}
	}

	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Install Node.js 22 LTS
curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
apt-get install -y nodejs

# Install Hanzo Bot
npm install -g @hanzo/bot

# Write environment configuration
cat > /etc/hanzo-bot.env << 'ENVEOF'
BOT_NODE_GATEWAY_URL=%s
BOT_GATEWAY_TOKEN=%s
HANZO_API_KEY=%s
AGENT_NODE_ID=%s
AGENT_DISPLAY_NAME=%s
HANZO_PLAYGROUND_CLOUD_NODE=true
ENVEOF

# Create systemd service
cat > /etc/systemd/system/hanzo-bot.service << 'SVCEOF'
[Unit]
Description=Hanzo Bot Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/hanzo-bot.env
ExecStart=/usr/bin/npx @hanzo/bot node run --name %s
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF

# Enable and start the service
systemctl daemon-reload
systemctl enable --now hanzo-bot
`, gatewayURL, gatewayToken, apiKey, nodeID, displayName, nodeID)
}
