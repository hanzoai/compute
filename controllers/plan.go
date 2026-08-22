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

package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// The PLAN catalog: the resale tiers a brand sells (object/plan_seed.go). Five
// TYPED ops, so this noun is in the registry every projection reads — OpenAPI,
// MCP, CLI, the by-name call plane — rather than only on the wire.
//
// ONE noun, ONE address, and the METHOD carries the verb:
//
//	GET    /v1/plans          list a catalog's active plans
//	POST   /v1/plans          add a plan to it
//	GET    /v1/plans/:name    read one
//	PUT    /v1/plans/:name    replace one
//	DELETE /v1/plans/:name    remove one (204)
//
// It used to spell each of those as its own address — get-plans, get-plan,
// add-plan, update-plan, delete-plan — so a client held five URLs for one
// resource and the address changed when the operation did. Those five are
// retired (routers/gone_plans.go): 410, naming this collection.
//
// A plan's identity is the PAIR (owner, name), so both are addressed: `?owner`
// names the catalog on every op, and the path segment names the plan within it.
// The URL is the addressing authority — zip binds body, then query, then path,
// in increasing authority — so a body claiming a different owner or name
// updates the plan the URL named, not the one it claims to be.

// Plans is one catalog's plans.
type Plans struct {
	// Plans is one row per active plan, in catalog order.
	Plans []*object.Plan `json:"plans"`
}

// PlanRef addresses one plan: the catalog from `?owner`, the plan from the path.
type PlanRef struct {
	// Name is the plan's name within the catalog, from the URL path.
	Name string `json:"-" url:"name"`
	caller
}

// PlanWrite is a plan as a caller writes it. The plan IS the body — visor's
// model is the wire shape and has been since this route existed — with the
// catalog and the plan's name taken from the URL, which outranks both.
type PlanWrite struct {
	object.Plan
	// Authorization is the forwarded IAM Bearer. Declared here rather than
	// embedded through caller, because caller carries an Owner that would
	// collide with the plan's own.
	Authorization string `json:"-" header:"Authorization"`
}

// plan resolves and authorizes the plan an op is about to touch, and returns the
// trimmed pair that addresses it.
//
// It is what makes these ops safe to reach BY NAME as well as over HTTP:
// ApiFilter authorizes a REQUEST, and an in-process call has none. The rule is
// the same object-level tenant guard the machine ops apply (authorize) — the
// service subject, a global admin, or a subject whose org owns the object — so
// one policy covers both nouns rather than two that can drift.
func plan(authorization string, owner string, name string) (string, string, error) {
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
	if owner == "" || name == "" {
		return "", "", zip.ErrBadRequest("owner and plan name are required")
	}
	if err := authorize(object.GetBearerUser(authorization), util.GetIdFromOwnerAndName(owner, name)); err != nil {
		return "", "", err
	}
	return owner, name, nil
}

// ListPlans returns the active plans of one catalog, in catalog order.
//
// Response: {"plans": [{"owner": "hanzo", "name": "starter", "priceMonthly": 500}]}
func ListPlans(_ context.Context, in *Scope) (*Plans, error) {
	// "*" is the whole catalog rather than one plan, exactly as the agent list
	// addresses an org's bindings: the guard judges the OWNER, and a global admin
	// or the service subject may read any.
	owner, _, err := plan(in.Authorization, in.Owner, "*")
	if err != nil {
		return nil, err
	}
	plans, err := object.GetPlans(owner)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if plans == nil {
		plans = []*object.Plan{}
	}
	return &Plans{Plans: plans}, nil
}

// GetPlan returns one plan of one catalog, or 404 when the catalog has no plan
// by that name.
//
// Absent is 404 and not a 200 carrying null: a caller that has to inspect the
// fields of a success to learn it got nothing is one that will forget to.
func GetPlan(_ context.Context, in *PlanRef) (*object.Plan, error) {
	owner, name, err := plan(in.Authorization, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	p, err := object.GetPlan(owner, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if p == nil {
		return nil, zip.ErrNotFound("no plan by that name")
	}
	return p, nil
}

// CreatePlan adds a plan to a catalog and answers with the plan as stored.
func CreatePlan(_ context.Context, in *PlanWrite) (*object.Plan, error) {
	owner, name, err := plan(in.Authorization, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	p := in.Plan
	p.Owner, p.Name = owner, name
	if _, err := object.AddPlan(&p); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return &p, nil
}

// ReplacePlan replaces one plan of one catalog and answers with the plan as
// stored. PUT rather than POST because it writes every column: the plan that
// comes back is the plan that was sent, not a merge of it with what was there.
//
// A plan that does not exist is 404 rather than a silent no-op. The store's
// update reports success either way, so nothing downstream can tell a caller
// that its write landed nowhere.
func ReplacePlan(_ context.Context, in *PlanWrite) (*object.Plan, error) {
	owner, name, err := plan(in.Authorization, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	existing, err := object.GetPlan(owner, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if existing == nil {
		return nil, zip.ErrNotFound("no plan by that name")
	}
	p := in.Plan
	p.Owner, p.Name = owner, name
	if _, err := object.UpdatePlan(owner, name, &p); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return &p, nil
}

// DeletePlan removes one plan from a catalog and answers 204.
//
// Idempotent: a plan that was already gone is in the state the caller asked
// for, so it answers 204 too. The old wire said "Affected"/"Unaffected" in a
// 200 body, which made every caller parse prose to learn a distinction none of
// them acted on.
func DeletePlan(_ context.Context, in *PlanRef) (*struct{}, error) {
	owner, name, err := plan(in.Authorization, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	if _, err := object.DeletePlan(&object.Plan{Owner: owner, Name: name}); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}
