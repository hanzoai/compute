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

package object

import (
	"strings"
	"testing"
)

func TestIsActiveMachineState(t *testing.T) {
	active := []string{"Running", "running", "RUNNING"}
	for _, s := range active {
		if !isActiveMachineState(s) {
			t.Errorf("isActiveMachineState(%q) = false, want true", s)
		}
	}
	inactive := []string{"Stopped", "Starting", "new", "off", "deleted", "", "pending"}
	for _, s := range inactive {
		if isActiveMachineState(s) {
			t.Errorf("isActiveMachineState(%q) = true, want false", s)
		}
	}
}

func TestIsTerminalMachineState(t *testing.T) {
	terminal := []string{"deleted", "Deleted", "archive", "errored", "error", "failed", "terminated"}
	for _, s := range terminal {
		if !isTerminalMachineState(s) {
			t.Errorf("isTerminalMachineState(%q) = false, want true", s)
		}
	}
	// Stopped/Starting are provisioning-recoverable, NOT terminal — they must
	// map to Pending, never Error.
	nonTerminal := []string{"Running", "Stopped", "Starting", "new", "off", ""}
	for _, s := range nonTerminal {
		if isTerminalMachineState(s) {
			t.Errorf("isTerminalMachineState(%q) = true, want false", s)
		}
	}
}

func TestMachineHasBotRuntimeTag(t *testing.T) {
	cases := []struct {
		name  string
		tag   string
		agent string
		want  bool
	}{
		{"exact match", "managed-by:hanzo-visor,hanzo-bot:researcher,", "researcher", true},
		{"with spaces", "os:linux, hanzo-bot:researcher , display-name:x", "researcher", true},
		{"case insensitive", "HANZO-BOT:Researcher", "researcher", true},
		{"wrong agent", "hanzo-bot:writer,", "researcher", false},
		{"no bot tag", "managed-by:hanzo-visor,os:linux", "researcher", false},
		{"empty tag", "", "researcher", false},
		{"substring must not match", "hanzo-bot:researcher-2", "researcher", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Machine{Tag: tc.tag}
			if got := machineHasBotRuntimeTag(m, tc.agent); got != tc.want {
				t.Errorf("machineHasBotRuntimeTag(tag=%q, agent=%q) = %v, want %v", tc.tag, tc.agent, got, tc.want)
			}
		})
	}
}

// TestSplitMachineIdNeverPanics locks the object-layer id split as fail-closed.
// The prior code called util.GetOwnerAndNameFromId (panics on != 2 tokens), so a
// malformed machine id reaching BindAgent/GetAgentBinding/UnbindAgent crashed
// the request. splitMachineId must return empties instead.
func TestSplitMachineIdNeverPanics(t *testing.T) {
	cases := []struct {
		id    string
		owner string
		name  string
	}{
		{"hanzoai/m1", "hanzoai", "m1"},
		{"m1", "", ""},
		{"", "", ""},
		{"a/b/c", "", ""},
		{"/m1", "", ""},
		{"hanzoai/", "", ""},
	}
	for _, tc := range cases {
		o, n := splitMachineId(tc.id)
		if o != tc.owner || n != tc.name {
			t.Errorf("splitMachineId(%q) = (%q,%q), want (%q,%q)", tc.id, o, n, tc.owner, tc.name)
		}
	}
}

func TestReconcileBindingStatus(t *testing.T) {
	binding := func(agent string) *AgentBinding {
		return &AgentBinding{Org: "hanzoai", AgentName: agent}
	}

	cases := []struct {
		name        string
		binding     *AgentBinding
		machine     *Machine
		wantStatus  string
		msgContains string
	}{
		{
			name:        "nil machine -> Error",
			binding:     binding("researcher"),
			machine:     nil,
			wantStatus:  AgentBindingError,
			msgContains: "not found",
		},
		{
			name:        "terminal machine -> Error",
			binding:     binding("researcher"),
			machine:     &Machine{State: "deleted"},
			wantStatus:  AgentBindingError,
			msgContains: "terminal",
		},
		{
			name:        "starting machine -> Pending",
			binding:     binding("researcher"),
			machine:     &Machine{State: "Starting"},
			wantStatus:  AgentBindingPending,
			msgContains: "provisioning",
		},
		{
			name:        "stopped machine -> Pending",
			binding:     binding("researcher"),
			machine:     &Machine{State: "Stopped"},
			wantStatus:  AgentBindingPending,
			msgContains: "provisioning",
		},
		{
			name:        "active but no bot tag -> Pending",
			binding:     binding("researcher"),
			machine:     &Machine{State: "Running", Tag: "managed-by:hanzo-visor,"},
			wantStatus:  AgentBindingPending,
			msgContains: "runtime tag",
		},
		{
			name:        "active with matching bot tag -> Bound",
			binding:     binding("researcher"),
			machine:     &Machine{State: "Running", Tag: "hanzo-bot:researcher,"},
			wantStatus:  AgentBindingBound,
			msgContains: "researcher",
		},
		{
			name:        "active with wrong agent tag -> Pending",
			binding:     binding("researcher"),
			machine:     &Machine{State: "Running", Tag: "hanzo-bot:writer,"},
			wantStatus:  AgentBindingPending,
			msgContains: "runtime tag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := reconcileBindingStatus(tc.binding, tc.machine)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q (msg=%q)", status, tc.wantStatus, msg)
			}
			if !strings.Contains(strings.ToLower(msg), strings.ToLower(tc.msgContains)) {
				t.Errorf("message %q does not contain %q", msg, tc.msgContains)
			}
		})
	}
}

// TestReconcileNeverInventsBound is the load-bearing safety property: no machine
// state other than (Active AND matching bot tag) may ever produce Bound. This
// guards against the honest-state contract regressing into optimistic reporting.
func TestReconcileNeverInventsBound(t *testing.T) {
	b := &AgentBinding{Org: "hanzoai", AgentName: "researcher"}
	notBoundMachines := []*Machine{
		nil,
		{State: "Starting"},
		{State: "Stopped"},
		{State: "deleted"},
		{State: "Running"}, // active, no tag
		{State: "Running", Tag: "hanzo-bot:other"},       // active, wrong agent
		{State: "Starting", Tag: "hanzo-bot:researcher"}, // right tag but not active
	}
	for i, m := range notBoundMachines {
		status, msg := reconcileBindingStatus(b, m)
		if status == AgentBindingBound {
			t.Errorf("case %d: reconcile invented Bound for a non-bound machine (msg=%q)", i, msg)
		}
	}
	// The one and only Bound path.
	status, _ := reconcileBindingStatus(b, &Machine{State: "Running", Tag: "hanzo-bot:researcher,"})
	if status != AgentBindingBound {
		t.Errorf("the canonical bound machine did not reconcile to Bound, got %q", status)
	}
}
