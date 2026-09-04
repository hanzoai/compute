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
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/compute/conf"
)

// playground.go — a launched bot IS a playground node. registerPlaygroundNode
// plants each launched bot in the Hanzo playground node registry
// (<playground>/v1/nodes) at launch so it appears there attributed to its org —
// the same registry the bot's own @hanzo/bot runtime heartbeats into once up.
//
// compute is the org-attribution AUTHORITY: it resolved the org from the fleet
// launch (server-injected, never trusted from the client body), so it is compute
// — not the external @hanzo/bot runtime — that stamps the node's org. The
// runtime keeps the node live via heartbeats; compute registers it, already
// scoped to the right org, the moment the machine is created. This is why a bot
// appears in its org's space immediately, independent of what token (if any)
// the runtime later presents to the gateway.
//
// Best-effort by design (mirrors registerBotUser): a registration failure never
// fails the launch — the machine + bot still run, and the node reconciles when
// the runtime heartbeats. Idempotent: the playground upserts by node id, so a
// re-launch of a same-named fleet member no-ops on the existing node.
//
// Honest status: it does NOT assert health — the node is registered with the
// registry's default (unknown / starting). The @hanzo/bot runtime promotes it
// to active once it actually heartbeats, so the registry never claims a bot is
// healthy before it is.
func registerPlaygroundNode(org, name string) {
	if org == "" || name == "" {
		return
	}
	body, err := json.Marshal(map[string]any{
		"id":              name,
		"org_id":          org,
		"deployment_type": "long_running",
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, playgroundEndpoint()+"/v1/nodes/register", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// playgroundEndpoint is the base URL of the Hanzo playground node registry. Env
// (PLAYGROUND_URL) wins for local/dev overrides, then the app config
// (playgroundEndpoint), then the production default. The trailing slash is
// trimmed so the "/v1/nodes/register" join is always exact.
func playgroundEndpoint() string {
	e := strings.TrimSpace(os.Getenv("PLAYGROUND_URL"))
	if e == "" {
		e = strings.TrimSpace(conf.GetConfigString("playgroundEndpoint"))
	}
	if e == "" {
		e = "https://playground.hanzo.ai"
	}
	return strings.TrimRight(e, "/")
}
