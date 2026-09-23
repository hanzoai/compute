// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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
	"fmt"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
)

// botuser.go — a launched bot IS an org member. registerBotUser creates an IAM
// user for each machine at launch so the bot shows up as a user everywhere IAM
// identities surface — notably the hanzo.team roster — instead of on a separate
// bot dashboard. The bot is a passwordless "service-account" tagged "agent": it
// never logs in interactively; it authenticates to the gateway by token.
//
// Best-effort by design: a registration failure never fails the launch (the
// machine + bot still run; the identity reconciles later). Idempotent — a
// re-launch of a same-named fleet member no-ops on the existing user. IAM is
// configured at startup (controllers.InitAuthConfig -> iamsdk.InitConfig), so the
// global client is ready by the time any launch runs.
func registerBotUser(org, name, displayName string) {
	if org == "" || name == "" {
		return
	}
	if displayName == "" {
		displayName = name
	}
	_, _ = iamsdk.AddUser(&iamsdk.User{
		Owner:       org,
		Name:        name,
		DisplayName: displayName,
		Type:        "service-account",
		Tag:         "agent",
	})
}

// buildBotUserData generates a cloud-init script that installs @hanzo/bot
// on the machine and configures it as a systemd service connecting to the
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
	// org is the authoritative attribution tag the launch set on the spec —
	// surfaced to the runtime so the bot gateway connection
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
