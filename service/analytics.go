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

// analytics.go is the ONE path by which visor rolls compute fleet/spend events
// into hanzoai/datastore (hanzo.compute_usage, ClickHouse) — the ANALYTICAL plane
// that mirrors, but is orthogonal to, the OPERATIONAL commerce ledger
// (tenant-data-hierarchy HIP). Commerce metering DEBITS an org's balance; this
// only RECORDS a fleet event for unified, cross-tenant rollups that
// admin.hanzo.ai reads by org / app / project — and by kind across visor's
// compute spectrum (machine, bot, cluster, nodepool, container, function).
//
// Every emit is best-effort and fire-and-forget: the write runs in its own
// goroutine on a short-lived context and swallows all errors, so an unreachable
// or slow datastore NEVER blocks or fails a launch, hourly sweep, or destroy.
// Ingest is the datastore's ClickHouse HTTP interface (INSERT ... FORMAT
// JSONEachRow on the same DATASTORE_URL surface the other emitters use), so there
// is no new client dependency — net/http only.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/beego/beego/logs"
)

// Compute event kinds — the values of the `event` column. A launched row is
// written at provision, a running row each hour a machine stays up (alongside the
// recurring meter), and a destroyed row at teardown.
const (
	ComputeLaunched  = "launched"
	ComputeRunning   = "running"
	ComputeDestroyed = "destroyed"
)

// Compute kinds — the values of the `kind` LowCardinality(String) lens in
// hanzo.compute_usage, spanning visor's compute spectrum:
//
//	machine   — a raw droplet/VM (no agent)
//	bot       — a machine running the @hanzo/bot agent (gw.hanzo.bot)
//	cluster   — a K8s cluster (service/doks.go)
//	nodepool  — a K8s node pool (object/node_pool.go)
//	container — a container workload
//	function  — a FaaS function (hanzoai/functions)
//
// Every bot is a machine with the agent role; not every machine is a bot.
// admin.hanzo.ai renders one lens per kind over the one table. Only machine and
// bot emit today; cluster/nodepool/container/function land later on this SAME
// table + kind — no schema migration needed (the column is open-ended).
const (
	KindMachine   = "machine"
	KindBot       = "bot"
	KindCluster   = "cluster"
	KindNodePool  = "nodepool"
	KindContainer = "container"
	KindFunction  = "function"
)

// knownKinds is the recognized compute spectrum; CanonicalKind falls back to
// machine (the base of the spectrum — every kind is at least a machine's worth of
// compute) for anything outside it.
var knownKinds = map[string]bool{
	KindMachine:   true,
	KindBot:       true,
	KindCluster:   true,
	KindNodePool:  true,
	KindContainer: true,
	KindFunction:  true,
}

// Tenant-hierarchy + kind tag keys carried on a machine's DO tags. hanzo-org
// (orgTagKey, compute.go) is the authoritative billing/attribution key injected
// at launch; hanzo-app / hanzo-project are optional finer scoping a caller may
// set; hanzo-kind records the compute kind. All are read back here so a machine
// self-describes its org > app > project scope and kind on every sweep/destroy —
// the same tag read-back model as org.
const (
	appTagKey     = "hanzo-app"
	projectTagKey = "hanzo-project"
	kindTagKey    = "hanzo-kind"
)

// computeUsageTable is the datastore table analytics.go writes; fully qualified
// with the datastore DB at emit time.
const computeUsageTable = "compute_usage"

// ComputeEvent is one row of hanzo.compute_usage. The json tags are the exact
// ClickHouse column names (JSONEachRow maps by key), making this struct the
// single Go mirror of the schema in hanzoai/datastore (hanzo/schema.sql). ts is
// pre-formatted in ClickHouse's default DateTime input format so no per-request
// parsing setting is needed; kind is a plain LowCardinality(String) value.
type ComputeEvent struct {
	Org        string `json:"org"`
	App        string `json:"app"`
	Project    string `json:"project"`
	Kind       string `json:"kind"`
	Event      string `json:"event"`
	MachineID  string `json:"machine_id"`
	Size       string `json:"size"`
	PriceCents int64  `json:"price_cents"`
	Ts         string `json:"ts"`
}

// CanonicalKind normalizes an arbitrary kind string to the known compute
// spectrum, falling back to machine for anything unrecognized (including empty).
// Applied on every WRITE (SetKind) and READ (EmitCompute), so a missing or garbage
// tag safely resolves to machine and the LowCardinality column only ever sees a
// known value. Fleet launches set bot explicitly; a raw single launch carries no
// kind and so resolves to machine.
func CanonicalKind(kind string) string {
	if k := strings.TrimSpace(kind); knownKinds[k] {
		return k
	}
	return KindMachine
}

// SetKind records the launch's kind on the spec's tags so it flows onto the
// droplet and is recovered — via the same tag read-back as org/app/project — by
// the emit, sweep, and destroy paths. It canonicalizes the value and inits the tag
// map if needed. Both launch surfaces (single-machine compute.go and fleet.go)
// call it, so kind is set exactly one way.
func SetKind(spec *CreateMachineSpec, kind string) {
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Tags[kindTagKey] = CanonicalKind(kind)
}

// SetScope records the launch's OPTIONAL app/project scope on the spec's tags so
// it flows onto the droplet and is recovered — via the same tag read-back as
// org/kind (tagValue) — by the emit, sweep, and destroy paths (EmitCompute reads
// hanzo-app / hanzo-project into ComputeEvent.App/Project). Beneath org in the
// org > app > project hierarchy, both are optional: an empty value, or one
// carrying the tag read-back's "," / ":" separators (guarded by safeTagField
// exactly as org's attribution tag is), is skipped — so a launch that omits them
// is never broken and no unparseable tag is ever emitted. Both launch surfaces set
// scope exactly one way through here, mirroring SetKind.
func SetScope(spec *CreateMachineSpec, app, project string) {
	setScopeTag(spec, appTagKey, app)
	setScopeTag(spec, projectTagKey, project)
}

// setScopeTag sets one optional scope tag when its value is non-empty and safe for
// the comma/colon-joined tag read-back; it is the single guarded writer SetScope
// composes for both app and project.
func setScopeTag(spec *CreateMachineSpec, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" || !safeTagField(value) {
		return
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Tags[key] = value
}

// specIsBot reports whether a launch provisions the @hanzo/bot agent — true only
// for kind=bot. It is the single source of truth gating both the agent cloud-init
// (buildBotUserData) and the IAM agent-user registration (LaunchOrgMachine), so a
// machine (or any non-bot kind) is truthfully agent-less end to end.
func specIsBot(spec *CreateMachineSpec) bool {
	return CanonicalKind(spec.Tags[kindTagKey]) == KindBot
}

// MachineKind recovers a machine's canonical compute kind from its own tags —
// the read-back counterpart of SetKind (which writes the kind on launch). It is
// the ONE way to read a live machine's kind (the ?kind= list filter and
// EmitCompute both use it), so a missing/garbage tag resolves to machine.
func MachineKind(m *Machine) string {
	return CanonicalKind(tagValue(m.Tag, kindTagKey))
}

// MachineApp / MachineProject recover a machine's OPTIONAL app/project scope from
// its own tags — the read-back counterparts of SetScope, mirroring MachineKind.
// Empty when the machine carries no such tag (scope is optional). Used by the
// ?project= list filter and available to any scope-aware read.
func MachineApp(m *Machine) string     { return tagValue(m.Tag, appTagKey) }
func MachineProject(m *Machine) string { return tagValue(m.Tag, projectTagKey) }

// datastoreURL is the datastore's ClickHouse HTTP base (e.g.
// http://datastore.hanzo.svc.cluster.local:8123). It is the single "is the
// analytical plane wired" signal: unset ⇒ every emit is a safe no-op, so a
// deployment without a datastore is never blocked nor spammed with failed writes
// (mirrors how COMMERCE_SERVICE_TOKEN gates metering).
func datastoreURL() string { return strings.TrimSpace(os.Getenv("DATASTORE_URL")) }

// datastoreDB is the target database; defaults to the datastore's canonical
// `hanzo` database when DATASTORE_DB is unset.
func datastoreDB() string {
	if db := strings.TrimSpace(os.Getenv("DATASTORE_DB")); db != "" {
		return db
	}
	return "hanzo"
}

// AnalyticsConfigured reports whether compute events will actually be emitted —
// true exactly when DATASTORE_URL is set. Absent ⇒ EmitCompute is a no-op.
func AnalyticsConfigured() bool { return datastoreURL() != "" }

// tagValue recovers the value of key from a machine's comma-joined tag string (as
// getMachineFromDroplet builds it: "k1:v1,k2:v2,"). Empty when absent. This is
// the ONE tag read-back parser; orgFromTag is defined in terms of it.
func tagValue(tags, key string) string {
	want := key + ":"
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, want) {
			return strings.TrimPrefix(t, want)
		}
	}
	return ""
}

// EmitCompute records one fleet event for machine m into the datastore. org is
// the authoritative owner (from IAM on launch; recovered from the machine's own
// tag on the recurring/destroy paths); app/project/kind are recovered from m's
// tags; priceCents is the resale price for this event's hour (0 for destroyed). It
// is fire-and-forget: a no-op when analytics is unconfigured or m is nil,
// otherwise the write is handed to a goroutine so it NEVER delays or fails the
// caller.
func EmitCompute(org, event string, m *Machine, priceCents int64) {
	if m == nil {
		return
	}
	EmitComputeEvent(ComputeEvent{
		Org:        org,
		App:        tagValue(m.Tag, appTagKey),
		Project:    tagValue(m.Tag, projectTagKey),
		Kind:       MachineKind(m),
		Event:      event,
		MachineID:  m.Id,
		Size:       m.Size,
		PriceCents: priceCents,
	})
}

// EmitComputeEvent is the ONE kind-agnostic emit: it records a single compute
// fleet event of ANY kind (machine, bot, cluster, nodepool, container, function)
// into the datastore. It canonicalizes the kind and stamps ts when the caller
// left it empty, then — like every emit — is best-effort and fire-and-forget: a
// no-op when analytics is unconfigured, otherwise handed to a goroutine so it
// NEVER delays or fails the caller. EmitCompute (machine) and the cluster /
// nodepool emitters are all thin adapters over this one path.
func EmitComputeEvent(ev ComputeEvent) {
	if !AnalyticsConfigured() {
		return
	}
	ev.Kind = CanonicalKind(ev.Kind)
	if ev.Ts == "" {
		ev.Ts = time.Now().UTC().Format("2006-01-02 15:04:05.000")
	}
	go writeComputeEvent(ev)
}

// writeComputeEvent performs the single best-effort HTTP insert. Every failure is
// logged and swallowed — an analytical write must never surface to the caller. It
// runs on its own short-lived context so a hung datastore cannot leak a goroutine
// indefinitely.
func writeComputeEvent(ev ComputeEvent) {
	row, err := json.Marshal(ev)
	if err != nil {
		logs.Warning("compute analytics: marshal event: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, datastoreURL()+"/", bytes.NewReader(row))
	if err != nil {
		logs.Warning("compute analytics: build request: %v", err)
		return
	}
	q := req.URL.Query()
	q.Set("query", "INSERT INTO "+datastoreDB()+"."+computeUsageTable+" FORMAT JSONEachRow")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Content-Type", "application/json")
	// Hanzo Datastore auth headers (white-labeled from ClickHouse X-ClickHouse-*).
	if u := strings.TrimSpace(os.Getenv("DATASTORE_USER")); u != "" {
		req.Header.Set("X-Datastore-User", u)
	}
	if p := strings.TrimSpace(os.Getenv("DATASTORE_PASSWORD")); p != "" {
		req.Header.Set("X-Datastore-Key", p)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logs.Warning("compute analytics: emit %s for machine %s: %v", ev.Event, ev.MachineID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		logs.Warning("compute analytics: datastore rejected %s for machine %s: %s: %s", ev.Event, ev.MachineID, resp.Status, strings.TrimSpace(string(b)))
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
}
