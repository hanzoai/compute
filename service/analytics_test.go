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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capturedInsert struct {
	query string
	row   ComputeEvent
}

// captureDatastore stands up a fake datastore HTTP endpoint, points DATASTORE_URL
// at it, and returns a channel that receives the one parsed insert.
func captureDatastore(t *testing.T) <-chan capturedInsert {
	t.Helper()
	got := make(chan capturedInsert, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev ComputeEvent
		_ = json.Unmarshal(body, &ev)
		got <- capturedInsert{query: r.URL.Query().Get("query"), row: ev}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DATASTORE_URL", srv.URL)
	return got
}

// EmitCompute must POST an INSERT into hanzo.compute_usage whose row mirrors the
// machine: org authoritative, app/project/kind recovered from the tags, size and
// price carried through.
func TestEmitComputeInsertShape(t *testing.T) {
	got := captureDatastore(t)
	m := &Machine{
		Id:   "42",
		Size: "s-1vcpu-1gb",
		Tag:  "managed-by:hanzo-visor,hanzo-org:acme,hanzo-app:web,hanzo-project:api,hanzo-kind:bot,",
	}
	EmitCompute("acme", ComputeLaunched, m, 7)

	select {
	case c := <-got:
		if want := "INSERT INTO hanzo.compute_usage FORMAT JSONEachRow"; c.query != want {
			t.Fatalf("query = %q, want %q", c.query, want)
		}
		if c.row.Org != "acme" || c.row.App != "web" || c.row.Project != "api" {
			t.Fatalf("scope = %+v, want org=acme app=web project=api", c.row)
		}
		if c.row.Kind != KindBot {
			t.Fatalf("kind = %q, want %q", c.row.Kind, KindBot)
		}
		if c.row.Event != ComputeLaunched || c.row.MachineID != "42" || c.row.Size != "s-1vcpu-1gb" || c.row.PriceCents != 7 {
			t.Fatalf("row = %+v", c.row)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// A machine (explicit hanzo-kind:machine) emits kind='machine'.
func TestEmitComputeMachineKind(t *testing.T) {
	got := captureDatastore(t)
	m := &Machine{Id: "7", Size: "s-2vcpu-2gb", Tag: "hanzo-org:acme,hanzo-kind:machine,"}
	EmitCompute("acme", ComputeDestroyed, m, 0)

	select {
	case c := <-got:
		if c.row.Kind != KindMachine {
			t.Fatalf("kind = %q, want %q", c.row.Kind, KindMachine)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// An out-of-set kind tag normalizes to machine on emit (open-ended column, safe
// fallback) — the case the widened spectrum must guarantee.
func TestEmitComputeUnknownKindNormalizes(t *testing.T) {
	got := captureDatastore(t)
	m := &Machine{Id: "9", Size: "s-1vcpu-1gb", Tag: "hanzo-org:acme,hanzo-kind:quantum-toaster,"}
	EmitCompute("acme", ComputeRunning, m, 3)

	select {
	case c := <-got:
		if c.row.Kind != KindMachine {
			t.Fatalf("kind = %q, want %q (unknown normalizes to machine)", c.row.Kind, KindMachine)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// With no DATASTORE_URL, emit is a safe no-op (must not panic or block).
func TestAnalyticsUnconfiguredNoOp(t *testing.T) {
	t.Setenv("DATASTORE_URL", "")
	if AnalyticsConfigured() {
		t.Fatal("AnalyticsConfigured must be false when DATASTORE_URL is empty")
	}
	EmitCompute("acme", ComputeRunning, &Machine{Id: "1", Tag: "hanzo-org:acme,"}, 1)
}

// CanonicalKind passes through the whole compute spectrum and normalizes anything
// unrecognized (including empty) to machine.
func TestCanonicalKind(t *testing.T) {
	cases := map[string]string{
		// the known spectrum passes through
		"machine": KindMachine, "bot": KindBot, "cluster": KindCluster,
		"nodepool": KindNodePool, "container": KindContainer, "function": KindFunction,
		" bot ": KindBot,
		// out of set / empty -> machine (the spectrum base)
		"": KindMachine, "MACHINE": KindMachine, "vm": KindMachine, "garbage": KindMachine,
	}
	for in, want := range cases {
		if got := CanonicalKind(in); got != want {
			t.Errorf("CanonicalKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// SetKind + specIsBot gate the agent cloud-init: only a bot gets it; a default
// (raw single launch) is a machine and gets none.
func TestSetKindGatesAgent(t *testing.T) {
	spec := &CreateMachineSpec{}

	SetKind(spec, "") // default -> machine (raw single launch, agent-less)
	if spec.Tags[kindTagKey] != KindMachine || specIsBot(spec) {
		t.Fatalf("default kind: tag=%q isBot=%v, want machine/false", spec.Tags[kindTagKey], specIsBot(spec))
	}
	if ud := buildBotUserData(spec); ud != "" {
		t.Fatalf("machine must get no cloud-init, got %d bytes", len(ud))
	}

	SetKind(spec, KindBot)
	if spec.Tags[kindTagKey] != KindBot || !specIsBot(spec) {
		t.Fatalf("bot kind: tag=%q isBot=%v, want bot/true", spec.Tags[kindTagKey], specIsBot(spec))
	}
	if ud := buildBotUserData(spec); !strings.Contains(ud, "@hanzo/bot") {
		t.Fatal("bot must get @hanzo/bot cloud-init")
	}

	// a non-bot kind from the wider spectrum is also agent-less
	SetKind(spec, KindCluster)
	if specIsBot(spec) || buildBotUserData(spec) != "" {
		t.Fatal("cluster kind must be agent-less")
	}
}

// EmitComputeEvent is the kind-agnostic path a cluster event flows through
// unchanged — kind canonicalized, ts stamped — the exact shape the widened
// Clusters board reads (org authoritative, cluster UUID as the unit id).
func TestEmitComputeEventClusterShape(t *testing.T) {
	got := captureDatastore(t)
	EmitComputeEvent(ComputeEvent{
		Org: "acme", Kind: KindCluster, Event: ComputeLaunched,
		MachineID: "do-cluster-uuid", Size: "nyc3",
	})

	select {
	case c := <-got:
		if c.row.Kind != KindCluster || c.row.Org != "acme" || c.row.MachineID != "do-cluster-uuid" || c.row.Size != "nyc3" {
			t.Fatalf("cluster row = %+v", c.row)
		}
		if c.row.Ts == "" {
			t.Fatal("ts must be stamped when the caller leaves it empty")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// A node-pool event carries its project + hourly price (from CostPerHour)
// through the same one path — kind=nodepool.
func TestEmitComputeEventNodePoolShape(t *testing.T) {
	got := captureDatastore(t)
	EmitComputeEvent(ComputeEvent{
		Org: "acme", Project: "prod", Kind: KindNodePool, Event: ComputeRunning,
		MachineID: "pool-123", Size: "s-4vcpu-8gb", PriceCents: 24,
	})

	select {
	case c := <-got:
		if c.row.Kind != KindNodePool || c.row.Project != "prod" || c.row.PriceCents != 24 {
			t.Fatalf("nodepool row = %+v", c.row)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// An out-of-set kind normalizes to machine on the kind-agnostic path too
// (the LowCardinality column only ever sees a known value).
func TestEmitComputeEventUnknownKindNormalizes(t *testing.T) {
	got := captureDatastore(t)
	EmitComputeEvent(ComputeEvent{Org: "acme", Kind: "quantum-toaster", Event: ComputeLaunched, MachineID: "x"})

	select {
	case c := <-got:
		if c.row.Kind != KindMachine {
			t.Fatalf("kind = %q, want %q", c.row.Kind, KindMachine)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// EmitComputeEvent is a safe no-op when analytics is unconfigured (must not
// panic or block).
func TestEmitComputeEventUnconfiguredNoOp(t *testing.T) {
	t.Setenv("DATASTORE_URL", "")
	EmitComputeEvent(ComputeEvent{Org: "acme", Kind: KindCluster, Event: ComputeLaunched})
}
