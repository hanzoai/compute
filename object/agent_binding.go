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
	"fmt"
	"strings"

	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
	"github.com/hanzoai/xorm/schemas"
)

// Agent binding lifecycle states. Honest — each reflects a real, observable
// condition, never an optimistic guess:
//
//   - Pending:      binding recorded; the machine's @hanzo/bot runtime has not
//     yet been confirmed running (machine still provisioning, or bot cloud-init
//     not yet observed on the instance tags).
//   - Bound:        the machine is Active AND is tagged as running the
//     @hanzo/bot runtime for this agent (the launch cloud-init path stamps a
//     `hanzo-bot:<agentName>` provider tag).
//   - Unbound:      the binding was explicitly deleted; retained transiently
//     only as a return value, never persisted (DELETE removes the row).
//   - Error:        the machine is in a terminal/failed cloud state, so the
//     runtime cannot be running.
const (
	AgentBindingPending = "Pending"
	AgentBindingBound   = "Bound"
	AgentBindingError   = "Error"
)

// BotRuntimeTag is the provider tag that marks a machine as running the
// @hanzo/bot runtime for a given agent. The launch cloud-init path is the single
// writer; reconcile is the single reader. The value is the agent name so one
// machine hosts exactly one agent's bot (the machine IS the bot's identity).
const BotRuntimeTag = "hanzo-bot"

// AgentBinding records that a machine runs the @hanzo/bot runtime for a specific
// cloud Agent. It is the vm half of the Bot lifecycle: a Bot = Agent
// (execution_mode=long-running) + a bound compute running @hanzo/bot. The
// operator's AgentDeployment controller drives this over
// `POST /v1/machines/:id/bind-agent`.
//
// The machine is the identity: PK is (Owner, Name) where Name == the machine id
// (`owner/machine-name`). One machine hosts one agent's bot runtime, so the
// binding is 1:1 with the machine — re-binding a different agent replaces the
// prior binding rather than layering.
type AgentBinding struct {
	Owner string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name  string `xorm:"varchar(100) notnull pk" json:"name"`
	// Project is the attribution dimension alongside Owner: the project WITHIN the
	// org this agent binding belongs to. Additive, Sync2-safe, defaults to "".
	Project     string `xorm:"varchar(100)" json:"project"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`

	// Bound cloud Agent identity. `Org` is the cloud/IAM organization that owns
	// the Agent (`<org>-<agent>` service-account naming); `AgentName` is the
	// Agent's name in the cloud `/v1/agents` registry.
	Org       string `xorm:"varchar(100)" json:"org"`
	AgentName string `xorm:"varchar(100)" json:"agentName"`

	// MachineId is the machine's id (`owner/machine-name`), duplicated from the
	// PK for a stable JSON field the operator can echo without re-splitting.
	MachineId string `xorm:"varchar(200)" json:"machineId"`
	// Provider + PublicIp are denormalized snapshots of the bound machine for
	// quick status reads without a second machine lookup.
	Provider string `xorm:"varchar(100)" json:"provider"`
	PublicIp string `xorm:"varchar(100)" json:"publicIp"`

	// BotVersion is the @hanzo/bot npm version the runtime targets. Empty means
	// "track the machine's launch default" (the cloud-init installs `@hanzo/bot`
	// latest at provision time); a pinned semver is recorded verbatim.
	BotVersion string `xorm:"varchar(100)" json:"botVersion"`

	// Status is the reconciled lifecycle state (Pending/Bound/Error). Message
	// carries the human-readable reason behind the current Status.
	Status  string `xorm:"varchar(100)" json:"status"`
	Message string `xorm:"varchar(500)" json:"message"`
}

// isActiveMachineState reports whether a machine's normalized State means the
// instance is up and can run the bot runtime. Providers normalize their raw
// status to "Running" in `getMachineFrom*`; that is the one active value.
func isActiveMachineState(state string) bool {
	return strings.EqualFold(state, "Running")
}

// isTerminalMachineState reports whether a machine is in a failed/gone state
// where the runtime can never be running. "Stopped"/"Starting" are NOT terminal
// (a stopped machine can start; a starting one is still provisioning), so they
// map to Pending, not Error.
func isTerminalMachineState(state string) bool {
	switch strings.ToLower(state) {
	case "deleted", "archive", "errored", "error", "failed", "terminated":
		return true
	default:
		return false
	}
}

// machineHasBotRuntimeTag reports whether the machine carries the
// `hanzo-bot:<agentName>` provider tag that the launch cloud-init stamps once
// the @hanzo/bot runtime is installed for that agent. `machine.Tag` is the
// comma-joined provider tag list.
func machineHasBotRuntimeTag(machine *Machine, agentName string) bool {
	want := fmt.Sprintf("%s:%s", BotRuntimeTag, agentName)
	for _, tag := range strings.Split(machine.Tag, ",") {
		if strings.EqualFold(strings.TrimSpace(tag), want) {
			return true
		}
	}
	return false
}

// resolveBoundMachine resolves the machine a binding targets from the RESELL
// compute surface — the Hanzo house DigitalOcean account, where a machine is a
// droplet identified by its integer id and owned by an org via the
// `hanzo-org:<org>` tag. This is the SAME source the bind routes are scoped to
// (`service.GetOrgMachine` verifies the droplet carries the caller org's tag),
// NOT the BYOC `object.Machine` table (whose owner semantics and identity are a
// different machine population). Using the resell source is what makes the
// binding operate on the machine the operator actually launched, and it enforces
// tenant isolation a second way: even with the id owner already pinned to the
// caller org, GetOrgMachine returns nil for a droplet that is not tagged to that
// org, so a binding can never attach to another tenant's machine.
//
// It never panics on caller input: a non-numeric droplet id yields a clean error
// from GetOrgMachine, and an unowned/absent droplet yields (nil, nil).
func resolveBoundMachine(owner string, name string) (*Machine, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	m, err := service.GetOrgMachine(owner, name)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	// Convert the service view to the object view. Resell machines are always in
	// the house DigitalOcean account, so Provider is fixed here (getMachineFromDroplet
	// leaves it empty). Only the fields reconcile + the denormalized snapshot need
	// are carried.
	return &Machine{
		Owner:       owner,
		Name:        m.Name,
		Id:          m.Id,
		Provider:    "digitalocean",
		DisplayName: m.DisplayName,
		Region:      m.Region,
		Size:        m.Size,
		Tag:         m.Tag,
		State:       m.State,
		PublicIp:    m.PublicIp,
		PrivateIp:   m.PrivateIp,
	}, nil
}

// splitMachineId splits an `owner/name` machine id without panicking on
// malformed input. `util.GetOwnerAndNameFromId` panics unless the id is exactly
// two tokens, and its NoCheck variant index-panics on a single token; callers
// here take machine ids that ultimately originate from HTTP input, so the split
// must fail closed (return empties) rather than crash the request.
func splitMachineId(machineId string) (string, string) {
	tokens := strings.Split(machineId, "/")
	if len(tokens) != 2 || tokens[0] == "" || tokens[1] == "" {
		return "", ""
	}
	return tokens[0], tokens[1]
}

func getAgentBinding(owner string, name string) (*AgentBinding, error) {
	if owner == "" || name == "" {
		return nil, nil
	}

	engine, err := EngineFor(owner)
	if err != nil {
		return nil, err
	}
	binding := AgentBinding{Owner: owner, Name: name}
	existed, err := engine.Get(&binding)
	if err != nil {
		return &binding, err
	}

	if existed {
		return &binding, nil
	}
	return nil, nil
}

// GetAgentBindings lists every binding owned by `owner`, newest first.
func GetAgentBindings(owner string) ([]*AgentBinding, error) {
	bindings := []*AgentBinding{}
	engine, err := EngineFor(owner)
	if err != nil {
		return bindings, err
	}
	err = engine.Desc("created_time").Find(&bindings, &AgentBinding{Owner: owner})
	if err != nil {
		return bindings, err
	}
	return bindings, nil
}

// reconcileBindingStatus derives the honest Status of a binding from the current
// cloud state of its machine. Pure over (binding, machine) so it is unit-tested
// without a database or provider. It never invents a `Bound` — it requires BOTH
// an Active machine AND the `hanzo-bot:<agentName>` runtime tag the launch
// cloud-init stamps.
func reconcileBindingStatus(binding *AgentBinding, machine *Machine) (status string, message string) {
	if machine == nil {
		return AgentBindingError, "machine not found for binding"
	}
	if isTerminalMachineState(machine.State) {
		return AgentBindingError, fmt.Sprintf("machine in terminal state: %s", machine.State)
	}
	if !isActiveMachineState(machine.State) {
		return AgentBindingPending, fmt.Sprintf("machine provisioning (state: %s); @hanzo/bot runtime not yet confirmed", machine.State)
	}
	if !machineHasBotRuntimeTag(machine, binding.AgentName) {
		return AgentBindingPending, "machine active; awaiting @hanzo/bot runtime tag (cloud-init install in progress)"
	}
	return AgentBindingBound, fmt.Sprintf("@hanzo/bot runtime running agent %q", binding.AgentName)
}

// AddAgentBinding inserts a binding. Callers set identity + timestamps first.
func AddAgentBinding(binding *AgentBinding) (bool, error) {
	engine, err := EngineFor(binding.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Insert(binding)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

// UpdateAgentBinding writes all columns of an existing binding by PK.
func UpdateAgentBinding(binding *AgentBinding) (bool, error) {
	engine, err := EngineFor(binding.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{binding.Owner, binding.Name}).AllCols().Update(binding)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

// BindAgent provisions/marks a machine as running the @hanzo/bot runtime for a
// cloud Agent. It is idempotent: binding a machine that is already bound to the
// same agent re-reconciles status; binding it to a DIFFERENT agent replaces the
// prior binding (the machine hosts exactly one agent's bot). The returned
// binding carries the freshly reconciled honest Status.
//
// It does not itself install the runtime — the runtime is installed by the
// machine's launch cloud-init, which the operator drives via a machine launch
// with a `hanzo-bot:<agent>` tag before calling this. BindAgent records the
// agent↔machine relation and reports the observed convergence state.
func BindAgent(machineId string, org string, agentName string, botVersion string) (*AgentBinding, error) {
	if agentName == "" || org == "" {
		return nil, fmt.Errorf("org and agentName are required to bind an agent")
	}

	owner, name := splitMachineId(machineId)
	if owner == "" || name == "" {
		return nil, fmt.Errorf("invalid machine id (expected owner/name): %q", machineId)
	}

	machine, err := resolveBoundMachine(owner, name)
	if err != nil {
		return nil, err
	}
	if machine == nil {
		return nil, fmt.Errorf("machine not found: %s", machineId)
	}

	now := util.GetCurrentTime()

	existing, err := getAgentBinding(owner, name)
	if err != nil {
		return nil, err
	}

	binding := &AgentBinding{
		Owner:       owner,
		Name:        name,
		MachineId:   machineId,
		Org:         org,
		AgentName:   agentName,
		BotVersion:  botVersion,
		Provider:    machine.Provider,
		PublicIp:    machine.PublicIp,
		UpdatedTime: now,
	}
	if existing != nil {
		binding.CreatedTime = existing.CreatedTime
	} else {
		binding.CreatedTime = now
	}

	binding.Status, binding.Message = reconcileBindingStatus(binding, machine)

	if existing != nil {
		if _, err := UpdateAgentBinding(binding); err != nil {
			return nil, err
		}
	} else {
		if _, err := AddAgentBinding(binding); err != nil {
			return nil, err
		}
	}
	return binding, nil
}

// ReconcileAgentBinding re-derives and persists the honest Status of an existing
// binding against the live machine state. Used by the GET path so a read always
// reflects current convergence, and returns nil when the machine has no binding.
func ReconcileAgentBinding(machineId string) (*AgentBinding, error) {
	owner, name := splitMachineId(machineId)
	binding, err := getAgentBinding(owner, name)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, nil
	}

	machine, err := resolveBoundMachine(owner, name)
	if err != nil {
		return nil, err
	}

	status, message := reconcileBindingStatus(binding, machine)
	if machine != nil {
		binding.PublicIp = machine.PublicIp
		binding.Provider = machine.Provider
	}
	if status != binding.Status || message != binding.Message {
		binding.Status = status
		binding.Message = message
		binding.UpdatedTime = util.GetCurrentTime()
		if _, err := UpdateAgentBinding(binding); err != nil {
			return nil, err
		}
	}
	return binding, nil
}

// UnbindAgent removes the binding for a machine. Returns whether a binding was
// present. Orthogonal to the machine lifecycle — it never deletes the machine.
func UnbindAgent(machineId string) (bool, error) {
	owner, name := splitMachineId(machineId)
	binding, err := getAgentBinding(owner, name)
	if err != nil {
		return false, err
	}
	if binding == nil {
		return false, nil
	}
	return DeleteAgentBinding(binding)
}

// DeleteAgentBinding removes a binding by PK (unbind). It does NOT touch the
// machine — unbinding severs the agent↔machine record; tearing down the machine
// is a separate DeleteMachine call so the two lifecycles stay orthogonal.
func DeleteAgentBinding(binding *AgentBinding) (bool, error) {
	engine, err := EngineFor(binding.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{binding.Owner, binding.Name}).Delete(&AgentBinding{})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
