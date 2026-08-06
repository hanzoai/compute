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

package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// A machine's AGENT: the record that it runs the @hanzo/bot runtime for one
// cloud Agent. Four TYPED ops — the entry in the registry that REST, OpenAPI,
// MCP and the CLI all project from, so the shape a caller sees is the shape the
// code states. They are the only typed ops on this surface; the rest of the
// controller package still answers in casibase's {status,msg,data} envelope,
// which is why cloud's client reads these four one way and everything else the
// other (cloud/apps/visor/client.go).
//
// ONE noun, ONE address, and the METHOD carries the verb:
//
//	PUT    /v1/machines/:id/agent    bind (idempotent — re-binding is the state asked for)
//	GET    /v1/machines/:id/agent    read, reconciled against live machine state
//	DELETE /v1/machines/:id/agent    unbind (204; the machine stays)
//	GET    /v1/machines/agents       list the caller org's bindings
//
// It used to spell one resource three ways — POST .../bind-agent to create, GET
// and DELETE .../agent-binding to read and remove, GET /v1/agent-bindings to
// list — so a caller had to learn that create lived at a different path from its
// own read. A machine hosts at most one agent, so this is a to-one sub-resource:
// singular at the member, plural at the collection.

// caller is the identity half every op here declares: the forwarded IAM Bearer
// that names the user, and the ?owner a service subject scopes itself with.
// Embedded untagged, so zip promotes both onto each input and binds them from
// the request — the same two inputs resolveComputeOrg reads for the rest of the
// compute surface, but STATED in the type instead of fetched from the request
// behind the document's back.
//
// Both are `json:"-"`: they ride the URL and the headers, never a body, so
// neither is a field a caller can also smuggle past the URL's authority.
type caller struct {
	// Owner is the org a SERVICE subject scopes itself to (?owner=). Ignored for
	// a signed-in user, whose org is its own Owner claim — see principal.
	Owner string `json:"-" url:"owner"`
	// Authorization is the forwarded IAM Bearer. Declaring it is what makes
	// reading it part of the contract rather than something done off to the side.
	Authorization string `json:"-" header:"Authorization"`
}

// AgentRef addresses one machine's agent binding.
type AgentRef struct {
	// Id is the machine's name within the caller's org, from the URL path. The
	// OWNER half of the machine id is never taken from the caller — see machine.
	Id string `json:"id"`
	caller
}

// AgentBind binds a cloud Agent to a machine.
type AgentBind struct {
	// Id is the machine to bind, from the URL path.
	Id string `json:"id"`
	// AgentName is the Agent in cloud's /v1/agents registry this machine runs.
	AgentName string `json:"agentName" validate:"required"`
	// BotVersion pins the @hanzo/bot npm version; empty tracks the machine's
	// launch default.
	BotVersion string `json:"botVersion"`
	caller
}

// AgentScope is the whole input of the list: the caller, and nothing else. The
// collection IS the caller's org, so there is nothing to address.
type AgentScope struct {
	caller
}

// Agents is every agent binding the caller org owns.
type Agents struct {
	// AgentBindings is one row per bound machine, newest first.
	AgentBindings []*object.AgentBinding `json:"agentBindings"`
}

// principal answers both halves of "who is asking" from the two things a request
// can carry them in, and it is the ONE rule for this: an authenticated
// principal's org is authoritative and NOT overridable by a client-supplied
// ?owner. An authenticated user with an empty Owner claim resolves to "" and the
// caller fails closed — it does NOT fall through to ?owner. Only an
// unauthenticated service/app call (Basic client-id/secret; no Bearer user) may
// pass ?owner, and ApiFilter has already authorized it as subOwner=="app".
func principal(authorization string, owner string) (*iamsdk.User, string) {
	if u := object.GetBearerUser(authorization); u != nil {
		return u, strings.TrimSpace(u.Owner)
	}
	return nil, strings.TrimSpace(owner)
}

// machine resolves a request into the fully-qualified `owner/name` machine id it
// addresses, IAM-native and spoof-proof: the OWNER is always the caller's
// resolved org (never a client-supplied field) and the NAME is the `:id` path
// segment. A user therefore cannot address another org's machine by crafting the
// path; the id it can build is always scoped to its own org.
//
// It returns the resolved user alongside so the guard below judges the SAME
// principal the id was built from, and it never panics on caller input: no org
// context or an empty `:id` is a clean 400 rather than a half-formed id.
func machine(c caller, id string) (*iamsdk.User, string, error) {
	name := strings.TrimSpace(id)
	if name == "" {
		return nil, "", zip.ErrBadRequest("machine id is required")
	}
	user, org := principal(c.Authorization, c.Owner)
	if org == "" {
		return nil, "", zip.ErrBadRequest("machine id is required")
	}
	return user, util.GetIdFromOwnerAndName(org, name), nil
}

// splitOwnerName splits an `owner/name` id without panicking on malformed input
// (unlike util.GetOwnerAndNameFromId which panics, and the NoCheck variant which
// index-panics on a single token). Returns ("","") unless the id is exactly two
// non-empty slash-separated tokens.
func splitOwnerName(id string) (string, string) {
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 || tokens[0] == "" || tokens[1] == "" {
		return "", ""
	}
	return tokens[0], tokens[1]
}

// authorize is the object-level tenant guard for these ops (defense in depth,
// applied by every one of them). The global ABAC ApiFilter admits these routes
// for any authenticated brand user via authz.isResellComputePath, deferring
// org-scoping to the handler — exactly the model the rest of the resell compute
// surface uses. This guard closes that gap here, mirroring authz.IsAllowed's
// subject model exactly:
//
//   - the trusted service/app subject (Basic clientId/clientSecret, already
//     authenticated by ApiFilter as subOwner=="app"; user==nil) → allow — it is
//     the operator, and its org is the ?owner it supplied;
//   - a signed-in global admin (`built-in` owner or IsAdmin) → allow;
//   - a signed-in user whose org owns the machine (user.Owner == objOwner) →
//     allow (structurally guaranteed, since machine builds objOwner FROM that
//     same Owner — the mismatch branch is belt-and-suspenders); anyone else /
//     forbidden / deleted → deny.
func authorize(user *iamsdk.User, machineId string) error {
	objOwner, _ := splitOwnerName(machineId)
	if objOwner == "" {
		return zip.ErrBadRequest("invalid machine id (expected owner/name)")
	}
	if user == nil {
		// Service/app subject: ApiFilter has already authenticated it as
		// subOwner=="app" (the only way an unauthenticated caller reaches this
		// handler), and its org == the ?owner it supplied via principal.
		return nil
	}
	if user.IsForbidden || user.IsDeleted {
		return zip.ErrForbidden("Unauthorized operation")
	}
	// Global admin: the `built-in` org or an explicit admin flag.
	if user.Owner == "built-in" || user.IsAdmin {
		return nil
	}
	if user.Owner == objOwner {
		return nil
	}
	return zip.ErrForbidden("Unauthorized operation: machine belongs to a different owner")
}

// BindAgent binds a cloud Agent to one of the caller org's machines: the machine
// is marked as running that Agent's @hanzo/bot runtime. The owning org is the
// validated tenant, never a client field.
//
// Idempotent — PUT, because re-binding the same agent to the same machine is the
// state the caller asked for, not a second binding; binding a DIFFERENT agent
// replaces the prior one, since a machine hosts exactly one agent's bot.
//
// It does not itself install the runtime: that is the machine's launch
// cloud-init. This records the agent↔machine relation and answers with the
// honest, reconciled convergence state.
//
// Example: {"agentName": "bot-a", "botVersion": "1.4.0"}
// Response: {"owner": "acme", "name": "drop-a", "machineId": "acme/drop-a", "agentName": "bot-a", "status": "Pending"}
func BindAgent(_ context.Context, in *AgentBind) (*object.AgentBinding, error) {
	user, machineId, err := machine(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, machineId); err != nil {
		return nil, err
	}
	agentName := strings.TrimSpace(in.AgentName)
	if agentName == "" {
		return nil, zip.ErrBadRequest("agentName is required")
	}
	org, _ := splitOwnerName(machineId)
	binding, err := object.BindAgent(machineId, org, agentName, strings.TrimSpace(in.BotVersion))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return binding, nil
}

// GetAgent returns the agent binding of one of the caller org's machines, its
// status re-reconciled against live machine state, or 404 when the machine runs
// no bot runtime.
//
// Response: {"owner": "acme", "name": "drop-a", "machineId": "acme/drop-a", "agentName": "bot-a", "status": "Bound"}
func GetAgent(_ context.Context, in *AgentRef) (*object.AgentBinding, error) {
	user, machineId, err := machine(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, machineId); err != nil {
		return nil, err
	}
	binding, err := object.ReconcileAgentBinding(machineId)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// Absent is 404 and not a 200 carrying an empty object: "this machine runs no
	// agent" is a fact about the machine, and a caller that has to inspect the
	// fields of a success to discover a miss is one that will forget to.
	if binding == nil {
		return nil, zip.ErrNotFound("no agent binding for machine")
	}
	return binding, nil
}

// UnbindAgent detaches the agent runtime from one of the caller org's machines
// and answers 204. The machine stays — this halts the bot, it does not terminate
// the compute, so the two lifecycles stay orthogonal.
//
// Idempotent: a machine that was not bound is already in the asked-for state, so
// it answers 204 too. The old wire said "Affected"/"Unaffected" in a 200 body,
// which made every caller parse prose to learn a distinction none of them acted
// on.
func UnbindAgent(_ context.Context, in *AgentRef) (*struct{}, error) {
	user, machineId, err := machine(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, machineId); err != nil {
		return nil, err
	}
	if _, err := object.UnbindAgent(machineId); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}

// ListAgents returns every agent↔machine binding in the caller's org — which
// machines run which cloud Agent, newest first, each with its reconciled status.
//
// Response: {"agentBindings": [{"owner": "acme", "name": "drop-a", "agentName": "bot-a", "status": "Bound"}]}
func ListAgents(_ context.Context, in *AgentScope) (*Agents, error) {
	// The org is the caller's resolved org (spoof-proof): a client-supplied
	// ?owner is honored only for the service/app subject, exactly as principal
	// governs the rest of the resell compute surface. A user can therefore only
	// ever list its OWN org's bindings.
	user, owner := principal(in.Authorization, in.Owner)
	if owner == "" {
		return nil, zip.ErrForbidden("unauthorized: no org context")
	}
	// Defense in depth: reuse the machine guard with a synthetic `owner/*` id
	// (the service credential and global admins may list any).
	if err := authorize(user, util.GetIdFromOwnerAndName(owner, "*")); err != nil {
		return nil, err
	}
	bindings, err := object.GetAgentBindings(owner)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if bindings == nil {
		bindings = []*object.AgentBinding{}
	}
	return &Agents{AgentBindings: bindings}, nil
}
