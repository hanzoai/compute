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
// into hanzoai/datastore (ClickHouse) — the ANALYTICAL plane that mirrors, but is
// orthogonal to, the OPERATIONAL commerce ledger (tenant-data-hierarchy HIP).
// Commerce metering DEBITS an org's balance; this only RECORDS a fleet event for
// unified, cross-tenant rollups that admin.hanzo.ai reads by org / app / project.
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

// Compute event kinds — the values of the `event` LowCardinality column. A
// launched row is written at provision, a running row each hour a machine stays
// up (alongside the recurring meter), and a destroyed row at teardown.
const (
	ComputeLaunched  = "launched"
	ComputeRunning   = "running"
	ComputeDestroyed = "destroyed"
)

// Tenant-hierarchy tag keys carried on a machine's DO tags. hanzo-org (orgTagKey,
// compute.go) is the authoritative billing/attribution key injected at launch;
// hanzo-app / hanzo-project are optional finer scoping a caller may set, read back
// here so analytics is groupable across the full org > app > project hierarchy.
const (
	appTagKey     = "hanzo-app"
	projectTagKey = "hanzo-project"
)

// computeEventsTable is the datastore table analytics.go writes; fully qualified
// with the datastore DB at emit time.
const computeEventsTable = "compute_events"

// ComputeEvent is one row of hanzo.compute_events. The json tags are the exact
// ClickHouse column names (JSONEachRow maps by key), making this struct the
// single Go mirror of the schema in hanzoai/datastore (hanzo/schema.sql). ts is
// pre-formatted in ClickHouse's default DateTime input format so no per-request
// parsing setting is needed.
type ComputeEvent struct {
	Org        string `json:"org"`
	App        string `json:"app"`
	Project    string `json:"project"`
	Event      string `json:"event"`
	MachineID  string `json:"machine_id"`
	Size       string `json:"size"`
	PriceCents int64  `json:"price_cents"`
	Ts         string `json:"ts"`
}

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
// tag on the recurring/destroy paths); app/project are recovered from m's tags;
// priceCents is the resale price for this event's hour (0 for destroyed). It is
// fire-and-forget: a no-op when analytics is unconfigured or m is nil, otherwise
// the write is handed to a goroutine so it NEVER delays or fails the caller.
func EmitCompute(org, event string, m *Machine, priceCents int64) {
	if !AnalyticsConfigured() || m == nil {
		return
	}
	go writeComputeEvent(ComputeEvent{
		Org:        org,
		App:        tagValue(m.Tag, appTagKey),
		Project:    tagValue(m.Tag, projectTagKey),
		Event:      event,
		MachineID:  m.Id,
		Size:       m.Size,
		PriceCents: priceCents,
		Ts:         time.Now().UTC().Format("2006-01-02 15:04:05.000"),
	})
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
	q.Set("query", "INSERT INTO "+datastoreDB()+"."+computeEventsTable+" FORMAT JSONEachRow")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Content-Type", "application/json")
	// ClickHouse HTTP auth (optional in dev; wired from the datastore secret in
	// cluster). Header form keeps credentials out of the query string.
	if u := strings.TrimSpace(os.Getenv("DATASTORE_USER")); u != "" {
		req.Header.Set("X-ClickHouse-User", u)
	}
	if p := strings.TrimSpace(os.Getenv("DATASTORE_PASSWORD")); p != "" {
		req.Header.Set("X-ClickHouse-Key", p)
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
