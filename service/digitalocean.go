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
	"net/http"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

type MachineDigitalOceanClient struct {
	Client *godo.Client
	region string
}

// Kubernetes is DigitalOcean's managed-cluster face, over the SAME godo client
// this machine client already holds. One credential, one transport, two nouns —
// which is what keeps there from being a second way to reach DigitalOcean.
func (c MachineDigitalOceanClient) Kubernetes() KubernetesClientInterface {
	return &DOKSClient{Client: c.Client}
}

// Volumes, Vpcs and LoadBalancers are DigitalOcean's other nouns over the SAME
// godo client and region. Each was a separate factory with its own list of cloud
// names; a capability on the one client is the same fact with nothing to drift.
func (c MachineDigitalOceanClient) Volumes() VolumeClientInterface {
	return &VolumeDigitalOceanClient{Client: c.Client, region: c.region}
}

func (c MachineDigitalOceanClient) Vpcs() VpcClientInterface {
	return &VpcDigitalOceanClient{Client: c.Client, region: c.region}
}

func (c MachineDigitalOceanClient) LoadBalancers() LoadBalancerClientInterface {
	return &LoadBalancerDigitalOceanClient{Client: c.Client, region: c.region}
}

// doAPITimeout bounds every call visor makes to DigitalOcean.
//
// An HTTP client with no timeout waits for as long as the socket stays open, and
// a provision waits UNDER the org's provisioning hold (service.Provision). So one
// hung api.digitalocean.com did not fail a request — it wedged that org's every
// subsequent provision behind a mutex nobody was going to release, with no error,
// no log and no recovery short of a restart. Bounding the call is what bounds the
// hold.
//
// newDOClient is the ONE DigitalOcean client constructor: token auth, bounded.
// Every DO surface visor drives — droplets, volumes, managed Kubernetes, cost —
// builds through it, so "a DigitalOcean call cannot hang forever" is one
// statement in one place rather than four that have to agree.
// newDOClient builds the godo client over the carried transport. When the token
// is empty the carrier is attaching it, so no oauth2 source is layered on — that
// is the whole point: this process has no key to layer.
func newDOClient(token string, hc *http.Client) *godo.Client {
	if hc == nil {
		hc = directHTTP()
	}
	if token == "" {
		return godo.NewClient(hc)
	}
	oc := oauth2.NewClient(context.WithValue(context.Background(), oauth2.HTTPClient, hc),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	oc.Timeout = providerTimeout
	return godo.NewClient(oc)
}

func newMachineDigitalOceanClient(accessKeySecret string, accessKeyId string, region string, hc *http.Client) (MachineDigitalOceanClient, error) {
	// DigitalOcean uses a single API token (passed as accessKeySecret).
	token := accessKeySecret
	if token == "" {
		token = accessKeyId
	}

	return MachineDigitalOceanClient{Client: newDOClient(token, hc), region: region}, nil
}

func getMachineFromDroplet(droplet godo.Droplet) *Machine {
	machine := &Machine{
		Name:        strconv.Itoa(droplet.ID),
		Id:          strconv.Itoa(droplet.ID),
		DisplayName: droplet.Name,
		CreatedTime: droplet.Created, // DO's RFC3339 create time — the launch-hour boundary the meter must not re-bill.
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
		size = DefaultLaunchSize
	}

	// Image: a numeric ImageID is a custom/user image (select by ID); anything
	// else is a distribution/application slug. Empty defaults to Ubuntu LTS.
	image := spec.ImageID
	var createImage godo.DropletCreateImage
	if id, err := strconv.Atoi(image); err == nil && id > 0 {
		createImage = godo.DropletCreateImage{ID: id}
	} else {
		if image == "" {
			image = "ubuntu-24-04-x64"
		}
		createImage = godo.DropletCreateImage{Slug: image}
	}

	tags := buildDropletTags(spec)
	// DigitalOcean drops droplet tags that do not already exist, so a NEW org's
	// first launch would otherwise create an untagged (untracked, unbillable)
	// droplet. Pre-create each tag (idempotent; an existing tag errors, ignored).
	for _, t := range tags {
		_, _, _ = client.Client.Tags.Create(context.TODO(), &godo.TagCreateRequest{Name: t})
	}

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
		Image:    createImage,
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

	// display-name/os are client-controlled too, so they get the SAME read-back
	// guard as free-form tags: a value with a "," / ":" could smuggle a second
	// hanzo-org token that orgFromTag would pick up. Drop it rather than emit an
	// unparseable tag.
	if spec.DisplayName != "" && safeTagField(spec.DisplayName) {
		tags = append(tags, fmt.Sprintf("display-name:%s", spec.DisplayName))
	}
	if spec.OS != "" && safeTagField(spec.OS) {
		tags = append(tags, fmt.Sprintf("os:%s", spec.OS))
	}

	for k, v := range spec.Tags {
		// Skip env: prefix tags — they are for cloud-init user data only,
		// not DigitalOcean resource tags (which also get stored in DB).
		if len(k) > 4 && k[:4] == "env:" {
			continue
		}
		// hanzo-org (billing key) and hanzo-project (billing attribution) are the
		// RESERVED attribution keys: the hourly meter reads them back from the
		// droplet to decide which org to debit and which project to attribute. A
		// client must never be able to set/forge/duplicate either. LaunchOrgMachine
		// injects the authoritative values under these map keys, but a value
		// carrying the meter's "," separator (or a second reserved token) could
		// smuggle a duplicate that orgFromTag/projectFromTag would pick up on
		// read-back. Drop the reserved keys from client input and refuse any tag
		// whose key/value could break the read-back parse — deterministic, not
		// reliant on DO's own tag validation.
		if k == orgTagKey || k == projectTagKey || !safeTagField(k) || !safeTagField(v) {
			continue
		}
		tags = append(tags, fmt.Sprintf("%s:%s", k, v))
	}

	// Attribution tags last, from the authoritative injected values, AFTER the
	// client loop dropped any client attempt — so exactly one hanzo-org and at
	// most one hanzo-project token exist and they are the server-resolved values.
	if org, ok := spec.Tags[orgTagKey]; ok {
		tags = append(tags, fmt.Sprintf("%s:%s", orgTagKey, org))
	}
	if project, ok := spec.Tags[projectTagKey]; ok && project != "" {
		tags = append(tags, fmt.Sprintf("%s:%s", projectTagKey, project))
	}

	return tags
}

// safeTagField rejects a tag key/value that could corrupt the comma-joined tag
// read-back the hourly meter parses (getMachineFromDroplet joins with "," and
// orgFromTag splits on ","). A "," would fabricate a tag boundary; a ":" in a
// value could fabricate a "key:value" pair. Empty is fine (dropped upstream).
func safeTagField(s string) bool {
	return !strings.ContainsAny(s, ",:")
}

// buildBotUserData generates a cloud-init script that installs @hanzo/bot
// on the droplet and configures it as a systemd service connecting to the
// gateway. Environment variables are passed via spec.Tags with the
// "env:" prefix (e.g., Tags["env:BOT_NODE_GATEWAY_URL"] = "wss://gw.hanzo.bot").
func buildBotUserData(spec *CreateMachineSpec) string {
	// A machine (kind=machine) is raw compute with no agent - no cloud-init.
	if !specIsBot(spec) {
		return ""
	}
	gatewayURL := "wss://gw.hanzo.bot"
	gatewayToken := ""
	apiKey := ""
	nodeID := spec.Name
	displayName := spec.DisplayName
	if displayName == "" {
		displayName = spec.Name
	}
	// org is the authoritative attribution tag LaunchOrgMachine injected before
	// CreateMachine — surfaced to the runtime so the bot gateway connection
	// and playground heartbeats are scoped to the SAME org visor registered the
	// node under (registerPlaygroundNode). Empty for a non-org launch.
	org := spec.Tags[orgTagKey]

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
HANZO_ORG=%s
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
`, gatewayURL, gatewayToken, apiKey, org, nodeID, displayName, nodeID)
}
