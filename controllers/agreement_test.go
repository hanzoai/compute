// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

package controllers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// A request is AUTHORIZED against one org and SERVED against another whenever
// the two read different fields, and that is the whole bug class this file is
// about.
//
// The authorization filter derives the object's owner from `?id=`, or from the
// request body on a write (routers/authz_filter.go getObject). It never reads
// `?owner` when an id is present. So a handler that read `?owner` was reading a
// field nothing had judged, and one request carrying both, disagreeing, was the
// whole exploit:
//
//	GET /v1/get-providers?owner=victim&id=attacker/anything
//
// The filter compared attacker to attacker and allowed it; the handler listed
// the victim's providers — a cross-tenant read of another org's cloud
// credentials, from any signed-in customer, in one GET. On get-machines the same
// shape was destructive as well as disclosing: the handler re-syncs from the
// named org's cloud, which DELETES the rows the cloud does not have.
//
// The fix is not to make the filter read `?owner` — it cannot know which field
// each handler will read. It is to leave the handlers exactly one place to get
// an org from, so there is nothing left to disagree with. Two tests hold that:
// TestOwnerIsNotAHandlerInput pins that ONE place structurally, and
// TestTheOrgServedIsTheCallersOwn drives every handler that has an org to scope
// and proves what comes back.

// probe is one owner-scoped read, with the two things a table needs to say about
// a noun: how to plant a row carrying a marker, and how to read that marker back
// out of the store afterwards.
type probe struct {
	// method and path are the route as routers.Route registers it.
	method string
	path   string
	// body is sent on a write. The org in it is the ATTACKER's, which is exactly
	// what makes the write authorized while the query names somebody else.
	body string
	// plant writes one row for org whose marker appears in an answer that serves
	// that org.
	plant func(t *testing.T, org, marker string)
	// stored reads the planted marker back, or "" when the row is gone. It is how
	// a destructive handler is caught: not serving another org's rows is only half
	// of leaving them alone.
	stored func(t *testing.T, org string) string
	// serves is false for the ONE handler that does not answer with the caller's
	// own rows: get-machines re-syncs from the caller's cloud before listing, and
	// a test org has no cloud, so the list it answers with is legitimately empty.
	serves bool
}

func stamp() string { return time.Now().Format(time.RFC3339) }

// probes is the table: every handler that has an org to scope, and for the reads
// that address a single row, the query that addresses it. Adding a handler that
// scopes by org means adding a row here — TestOwnerIsNotAHandlerInput is what
// makes forgetting that a build failure rather than a silent gap.
var probes = map[string]probe{
	"get-providers": {
		method: "GET", path: "/v1/get-providers", serves: true,
		plant: func(t *testing.T, org, marker string) {
			if _, err := object.AddProvider(&object.Provider{
				Owner: org, Name: marker, Category: "Cloud", Type: "DigitalOcean",
				State: "Active", ClientId: marker + "-token", CreatedTime: stamp(),
			}); err != nil {
				t.Fatalf("AddProvider(%s): %v", org, err)
			}
		},
		stored: func(t *testing.T, org string) string {
			rows, err := object.GetProviders(org)
			if err != nil {
				t.Fatalf("GetProviders(%s): %v", org, err)
			}
			return first(rows, func(p *object.Provider) string { return p.Name })
		},
	},
	"get-machines": {
		method: "GET", path: "/v1/get-machines", serves: false,
		plant: func(t *testing.T, org, marker string) {
			if _, err := object.AddMachine(&object.Machine{
				Owner: org, Name: marker, Id: marker, State: "Active",
				PublicIp: "203.0.113.7", CreatedTime: stamp(),
			}); err != nil {
				t.Fatalf("AddMachine(%s): %v", org, err)
			}
		},
		stored: func(t *testing.T, org string) string {
			rows, err := object.GetMachines(org)
			if err != nil {
				t.Fatalf("GetMachines(%s): %v", org, err)
			}
			return first(rows, func(m *object.Machine) string { return m.Name })
		},
	},
	"get-assets": {
		method: "GET", path: "/v1/get-assets", serves: true,
		plant: func(t *testing.T, org, marker string) {
			if _, err := object.AddAsset(&object.Asset{
				Owner: org, Name: marker, PublicIp: "203.0.113.8", RemotePassword: "hunter2",
				CreatedTime: stamp(),
			}); err != nil {
				t.Fatalf("AddAsset(%s): %v", org, err)
			}
		},
		stored: func(t *testing.T, org string) string {
			rows, err := object.GetAssets(org)
			if err != nil {
				t.Fatalf("GetAssets(%s): %v", org, err)
			}
			return first(rows, func(a *object.Asset) string { return a.Name })
		},
	},
	"get-sessions": {
		method: "GET", path: "/v1/get-sessions", serves: true,
		plant: func(t *testing.T, org, marker string) {
			if _, err := object.AddSession(&object.Session{
				Owner: org, Name: marker, Asset: marker, Protocol: "rdp",
				Status: object.NoConnect, StartTime: stamp(),
			}); err != nil {
				t.Fatalf("AddSession(%s): %v", org, err)
			}
		},
		stored: func(t *testing.T, org string) string {
			rows, err := object.GetSessions(org)
			if err != nil {
				t.Fatalf("GetSessions(%s): %v", org, err)
			}
			return first(rows, func(s *object.Session) string { return s.Name })
		},
	},
	"get-records": {
		method: "GET", path: "/v1/get-records", serves: true,
		plant: func(t *testing.T, org, marker string) {
			if !object.AddRecord(&object.Record{
				Name: marker, Organization: org, Action: marker, Method: "POST",
				CreatedTime: stamp(),
			}) {
				t.Fatalf("AddRecord(%s): no row written", org)
			}
		},
		stored: func(t *testing.T, org string) string {
			rows, err := object.GetRecords(org)
			if err != nil {
				t.Fatalf("GetRecords(%s): %v", org, err)
			}
			return first(rows, func(r *object.Record) string { return r.Name })
		},
	},
	"get-plans": {
		method: "GET", path: "/v1/get-plans", serves: true,
		plant:  plantPlan,
		stored: storedPlan,
	},
	"get-plan": {
		// The plan is addressed by NAME, and the name is the victim's: the read
		// must still answer out of the caller's own catalog.
		method: "GET", path: "/v1/get-plan?name=" + victimMark, serves: false,
		plant:  plantPlan,
		stored: storedPlan,
	},
	"update-plan": {
		// A write, so the filter judges the BODY — which names the attacker, and
		// is therefore authorized. The query names the victim's plan.
		method: "POST", path: "/v1/update-plan?name=" + victimMark, serves: false,
		body:   `{"owner":"` + attackerOrg + `","name":"` + victimMark + `","displayName":"OWNED","state":"Active"}`,
		plant:  plantPlan,
		stored: storedPlan,
	},
}

func plantPlan(t *testing.T, org, marker string) {
	t.Helper()
	if _, err := object.AddPlan(&object.Plan{
		Owner: org, Name: marker, DisplayName: marker, State: "Active",
		CreatedTime: stamp(),
	}); err != nil {
		t.Fatalf("AddPlan(%s): %v", org, err)
	}
}

func storedPlan(t *testing.T, org string) string {
	t.Helper()
	rows, err := object.GetPlans(org)
	if err != nil {
		t.Fatalf("GetPlans(%s): %v", org, err)
	}
	return first(rows, func(p *object.Plan) string { return p.DisplayName })
}

// first returns the marker of the first row, or "" for none.
func first[T any](rows []T, name func(T) string) string {
	if len(rows) == 0 {
		return ""
	}
	return name(rows[0])
}

const (
	victimOrg   = "victimread"
	attackerOrg = "attackerread"
	victimMark  = "victim-secret"
	attackMark  = "attacker-own"
)

// agreementWire stands the owner-scoped handlers up on a bare app, registered as
// routers.Route registers them.
func agreementWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	handler := func(fn func(*ApiController)) zip.Handler {
		return func(c *zip.Ctx) error { fn(New(c)); return nil }
	}
	app.Get("/v1/get-providers", handler((*ApiController).GetProviders))
	app.Get("/v1/get-machines", handler((*ApiController).GetMachines))
	app.Get("/v1/get-assets", handler((*ApiController).GetAssets))
	app.Get("/v1/get-sessions", handler((*ApiController).GetSessions))
	app.Get("/v1/get-records", handler((*ApiController).GetRecords))
	app.Get("/v1/get-plans", handler((*ApiController).GetPlans))
	app.Get("/v1/get-plan", handler((*ApiController).GetPlan))
	app.Post("/v1/update-plan", handler((*ApiController).UpdatePlan))
	return app
}

// TestTheOrgServedIsTheCallersOwn drives the cross-tenant probe at every one of
// them: the attacker's bearer, `?owner=` naming the victim, and `?id=` (or the
// body) naming the attacker so the authorization filter says yes.
//
// Three things are asserted, and the third is not implied by the first two: the
// victim's rows are not in the answer, the caller is still served its own, and
// the victim's rows are still THERE afterwards. A handler that re-syncs or writes
// under the org it was handed does not merely disclose — get-machines deleted
// every machine row the named org had.
func TestTheOrgServedIsTheCallersOwn(t *testing.T) {
	mint := signer(t, "https://test.id")
	app := agreementWire(t)

	for name, p := range probes {
		t.Run(name, func(t *testing.T) {
			victim, attacker := victimOrg+"-"+name, attackerOrg+"-"+name
			p.plant(t, victim, victimMark)
			p.plant(t, attacker, attackMark)

			// ?owner names the victim. ?id (a read) and the body (a write) name the
			// attacker, which is what the filter judges and allows.
			path := p.path + join(p.path) + "owner=" + victim + "&id=" + attacker + "/" + attackMark
			body := strings.ReplaceAll(p.body, attackerOrg, attacker)

			var answer string
			if p.method == "GET" {
				answer = get(t, app, path, mint(attacker))
			} else {
				answer = post(t, app, path, mint(attacker), body)
			}

			if strings.Contains(answer, victimMark) || strings.Contains(answer, victim) {
				t.Fatalf("%s served another org's rows: %s", name, answer)
			}
			if p.serves && !strings.Contains(answer, attackMark) {
				t.Fatalf("%s did not serve the caller's OWN rows, so this probe proves nothing: %s", name, answer)
			}
			if !p.serves && !isOk(answer) {
				t.Fatalf("%s did not answer, so this probe proves nothing: %s", name, answer)
			}
			if got := p.stored(t, victim); got != victimMark {
				t.Fatalf("%s changed another org's stored rows: marker %q, want %q", name, got, victimMark)
			}
		})
	}
}

// TestOwnerScopedReadsFailClosedWithoutAnOrg is the floor. No bearer and no
// service credential means no tenant, and a tenant-less read used to run with
// owner="" — which is not "nothing", it is a query for the rows of an org named
// the empty string, and on a shared table that is a real result set.
//
// A service call is still allowed to name its org (?owner with no bearer is the
// app path, authorized by ApiFilter as subOwner=="app"); what must refuse is a
// request that names no org at all.
func TestOwnerScopedReadsFailClosedWithoutAnOrg(t *testing.T) {
	app := agreementWire(t)

	for name, p := range probes {
		t.Run(name, func(t *testing.T) {
			var answer string
			if p.method == "GET" {
				answer = get(t, app, p.path, "")
			} else {
				answer = post(t, app, p.path, "", p.body)
			}
			if !strings.Contains(answer, "no org context") {
				t.Fatalf("a tenant-less %s must be refused, got %s", name, answer)
			}
		})
	}
}

// join is "?" or "&" depending on whether the route already carries a query.
func join(path string) string {
	if strings.Contains(path, "?") {
		return "&"
	}
	return "?"
}

// isOk reports whether the casibase envelope carries a success. A handler that
// refused is not evidence of scoping.
func isOk(answer string) bool {
	var env struct {
		Status string `json:"status"`
	}
	return json.Unmarshal([]byte(answer), &env) == nil && env.Status == "ok"
}

// TestOwnerIsNotAHandlerInput is the durable half, and the one that catches the
// NEXT handler rather than these eight.
//
// Every fix above is the same edit — stop reading `?owner`, ask resolveComputeOrg
// — and eight identical edits are eight chances for a ninth handler to be written
// the old way. There is exactly ONE place in this package that may read that
// field, and it is the rule itself; anywhere else is a second answer to whose org
// this is, judged by nothing.
func TestOwnerIsNotAHandlerInput(t *testing.T) {
	const rule = "compute.go" // resolveComputeOrg

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	reads := regexp.MustCompile(`Query\("owner"\)|FormValue\("owner"\)`)

	found := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if n := len(reads.FindAll(src, -1)); n > 0 {
			found[f] = n
		}
	}

	if len(found) != 1 || found[rule] != 1 {
		t.Fatalf("`?owner` is read at %v, want exactly once in %s (resolveComputeOrg). "+
			"A handler that reads it directly is authorized on one org and serves another — "+
			"take the org from resolveComputeOrg and add the handler to the probes table above",
			found, rule)
	}
}
