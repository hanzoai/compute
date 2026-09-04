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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The launched bot is stamped with its org in TWO places, both keyed off the
// authoritative org tag LaunchOrgMachine injects: the cloud-init env (so the
// runtime knows its org) and the server-side playground registration (so the
// node appears attributed to that org). These tests pin both.

// buildBotUserData must surface the injected org tag as HANZO_ORG in the
// cloud-init env — the fix for a bot booting with no org identity.
func TestBuildBotUserDataInjectsOrg(t *testing.T) {
	spec := &CreateMachineSpec{Name: "dave-000", DisplayName: "dave"}
	SetKind(spec, KindBot)
	spec.Tags[orgTagKey] = "maxpower"

	ud := buildBotUserData(spec)
	if !strings.Contains(ud, "HANZO_ORG=maxpower\n") {
		t.Fatalf("cloud-init must inject HANZO_ORG=maxpower, got:\n%s", ud)
	}
	// The node id the runtime reports must match the machine name so it upserts
	// the same registry node compute registered server-side.
	if !strings.Contains(ud, "AGENT_NODE_ID=dave-000\n") {
		t.Fatalf("cloud-init must inject AGENT_NODE_ID=dave-000, got:\n%s", ud)
	}
}

// A non-org bot launch injects an empty HANZO_ORG line (never a stray literal),
// so the runtime reads "" rather than a placeholder.
func TestBuildBotUserDataEmptyOrg(t *testing.T) {
	spec := &CreateMachineSpec{Name: "solo-000"}
	SetKind(spec, KindBot)

	ud := buildBotUserData(spec)
	if !strings.Contains(ud, "HANZO_ORG=\n") {
		t.Fatalf("empty-org bot must emit a blank HANZO_ORG line, got:\n%s", ud)
	}
}

// registerPlaygroundNode POSTs the node to <playground>/v1/nodes/register with
// the org in the body — the server-side registration that makes a launched bot
// appear in its org space regardless of the external runtime.
func TestRegisterPlaygroundNode(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	t.Setenv("PLAYGROUND_URL", srv.URL)
	registerPlaygroundNode("maxpower", "dave-000")

	if gotPath != "/v1/nodes/register" {
		t.Fatalf("register path = %q, want /v1/nodes/register", gotPath)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("register body not JSON: %v (%s)", err, gotBody)
	}
	if payload["id"] != "dave-000" || payload["org_id"] != "maxpower" {
		t.Fatalf("register body id/org_id = %v/%v, want dave-000/maxpower", payload["id"], payload["org_id"])
	}
	if payload["deployment_type"] != "long_running" {
		t.Fatalf("register body deployment_type = %v, want long_running", payload["deployment_type"])
	}
}

// Best-effort contract: a missing org or name is a no-op (never a stray POST),
// and an unreachable endpoint never panics — the launch must not fail on it.
func TestRegisterPlaygroundNodeNoop(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()
	t.Setenv("PLAYGROUND_URL", srv.URL)

	registerPlaygroundNode("", "dave-000")
	registerPlaygroundNode("maxpower", "")
	if hit {
		t.Fatal("empty org/name must not POST")
	}

	// Unreachable endpoint: must return quietly, not panic/block the launch.
	t.Setenv("PLAYGROUND_URL", "http://127.0.0.1:0")
	registerPlaygroundNode("maxpower", "dave-000")
}

// playgroundEndpoint prefers the env override and trims a trailing slash so the
// path join is exact; the production default holds when unset.
func TestPlaygroundEndpoint(t *testing.T) {
	t.Setenv("PLAYGROUND_URL", "https://pg.example.com/")
	if got := playgroundEndpoint(); got != "https://pg.example.com" {
		t.Fatalf("endpoint = %q, want trimmed https://pg.example.com", got)
	}
	t.Setenv("PLAYGROUND_URL", "")
	if got := playgroundEndpoint(); got != "https://playground.hanzo.ai" {
		t.Fatalf("default endpoint = %q, want https://playground.hanzo.ai", got)
	}
}
