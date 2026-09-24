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

// Package service — compute.go is Hanzo's hosted compute surface: machines Hanzo
// launches for an org in Hanzo's own EC2 account (ec2.go) and bills to that org.
// It is distinct from the "bring your own cloud" Provider path
// (object/machine_cloud.go), whose accounts are reached only through egress.
//
// Every operation is scoped to one org. A machine is found by its org and
// machine id tags, never by id alone, so a tenant can never read, stop or
// terminate another tenant's machine by guessing an id.
package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/hanzoai/compute/logs"
)

// orgTagKey/orgTag namespace resources by the Hanzo org that owns them.
const orgTagKey = "hanzo-org"

func orgTag(org string) string { return orgTagKey + ":" + org }

// ErrNoMachine is a hosted machine that the org does not have.
var ErrNoMachine = errors.New("no such hosted machine")

// ComputeConfigured reports whether the hosted account is configured at all, so
// callers answer "not configured" instead of a cryptic client error. It says
// nothing about whether the account answers; ComputeReachable does.
func ComputeConfigured() bool { return hostedConfig().Region != "" }

// ComputeReachable proves the hosted account's credentials work by spending one
// authenticated round trip on them.
//
// The two answers a caller must tell apart:
//
//	nil   — either the account is not configured (there are no hosted machines,
//	        so an empty answer is the TRUE one), or EC2 answered. Both mean proceed.
//	error — the account is configured and did not answer. Nothing about the
//	        hosted machines can be known this hour.
func ComputeReachable(ctx context.Context) error {
	if !ComputeConfigured() {
		return nil // nothing to ask
	}
	api, _, err := hostedEC2(ctx)
	if err != nil {
		return err
	}
	_, err = api.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters:    []ec2Types.Filter{filter("tag:"+managedByKey, managedBy)},
		MaxResults: aws.Int32(5),
	})
	if err != nil {
		return fmt.Errorf("hosted compute account unreachable: %w", err)
	}
	return nil
}

// bounded is a context for one hosted-account operation that has no caller
// deadline, so a hung API cannot hold an org's provisioning lease.
func bounded() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), providerTimeout)
}

// ---- org-scoped hosted machines ----

// ListOrgMachines returns org's hosted machines, optionally narrowed to one
// project. The org is a filter on EC2 and is checked again on every answer.
//
// project scopes the result WITHIN the org: the empty project returns every org
// machine, a named project only the machines carrying that hanzo-project tag.
// Project is an attribution and view dimension, not a second isolation boundary.
func ListOrgMachines(org, project string) ([]*Machine, error) {
	if !validOrgSlug(org) || !validFilterValue(org) {
		return nil, fmt.Errorf("invalid org %q", org)
	}
	filters := []ec2Types.Filter{
		filter("tag:"+orgTagKey, org),
		filter("instance-state-name", liveStates...),
	}
	if project != "" {
		if !validProjectSlug(project) || !validFilterValue(project) {
			return nil, fmt.Errorf("invalid project %q", project)
		}
		filters = append(filters, filter("tag:"+projectTagKey, project))
	}
	ctx, cancel := bounded()
	defer cancel()
	api, region, err := hostedEC2(ctx)
	if err != nil {
		return nil, err
	}
	found, err := describe(ctx, api, filters...)
	if err != nil {
		return nil, refusal("list machines", err, "", "")
	}
	out := make([]*Machine, 0, len(found))
	for _, inst := range found {
		m := machineFromInstance(inst, region)
		if m.Owner != org || m.Id == "" {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// GetOrgMachine returns org's hosted machine id, or nil when org has none by
// that id. An id that is not a hosted machine id is answered nil without a call.
func GetOrgMachine(org, id string) (*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	if !machineIDPattern.MatchString(id) {
		return nil, nil
	}
	ctx, cancel := bounded()
	defer cancel()
	api, region, err := hostedEC2(ctx)
	if err != nil {
		return nil, err
	}
	inst, err := findInstance(ctx, api, org, id)
	if err != nil || inst == nil {
		return nil, err
	}
	return machineFromInstance(*inst, region), nil
}

// DeleteOrgMachine terminates org's hosted machine id. ErrNoMachine when org has
// none by that id, answered without a call when id is not a hosted machine id. A
// terminated machine is no longer running, so the hourly meter stops with it.
func DeleteOrgMachine(org, id string) error {
	if !machineIDPattern.MatchString(id) {
		return ErrNoMachine
	}
	ctx, cancel := bounded()
	defer cancel()
	api, region, err := hostedEC2(ctx)
	if err != nil {
		return err
	}
	inst, err := findInstance(ctx, api, org, id)
	if err != nil {
		return err
	}
	if inst == nil {
		return ErrNoMachine
	}
	m := machineFromInstance(*inst, region)
	if err := terminate(ctx, api, inst); err != nil {
		return err
	}
	// Roll a destroyed event into the analytics datastore (best-effort; never
	// blocks or fails the delete).
	EmitCompute(org, ComputeDestroyed, m, 0)
	return nil
}

// SetOrgMachineState starts ("Running") or stops ("Stopped") org's hosted
// machine id, and reports whether anything changed. ErrNoMachine when org has
// none by that id, answered without a call when id is not a hosted machine id.
//
// A start is a provision: the org is authorized for the machine's first hour
// and debited it under the hour's meter id — the id the hourly sweep uses — unless
// the ledger already holds that hour billed (a launch or an earlier start in the
// same clock hour), so no hour is charged twice. A stop charges nothing: the hour
// the machine ran in is already paid.
func SetOrgMachineState(ctx context.Context, org, id, state string) (bool, error) {
	if !machineIDPattern.MatchString(id) {
		return false, ErrNoMachine
	}
	var want string
	switch {
	case strings.EqualFold(state, "Running"):
		want = "Running"
	case strings.EqualFold(state, "Stopped"):
		want = "Stopped"
	default:
		return false, fmt.Errorf("a hosted machine's state is Running or Stopped, not %q", state)
	}
	api, region, err := hostedEC2(ctx)
	if err != nil {
		return false, err
	}
	inst, err := findInstance(ctx, api, org, id)
	if err != nil {
		return false, err
	}
	if inst == nil {
		return false, ErrNoMachine
	}
	m := machineFromInstance(*inst, region)
	switch {
	case m.State == want:
		return false, nil
	case want == "Running" && m.State == "Stopped":
		cents, err := HourlyCents(m.Size)
		if err != nil {
			return false, err
		}
		at := time.Now()
		err = Provision(ctx, org, MachineProject(m), cents, cents, m.Size, func() (string, error) {
			if err := setState(ctx, api, inst, true); err != nil {
				return "", err
			}
			return startCharge(m.Id, at), nil
		}, func() { MarkBilled(m.Id, at) })
		return err == nil, err
	case want == "Stopped" && m.State == "Running":
		if err := setState(ctx, api, inst, false); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, fmt.Errorf("machine %s is %s; set it %s once that settles", id, m.State, want)
}

// LaunchReady reports why spec cannot launch, before anything is asked of
// commerce or the cloud: a size that is not for sale, a region that is not
// offered, an image or SSH key the account cannot honour, and every account
// setting that is missing, by name.
//
// The machine boots the configured image: a body naming its own image or SSH
// keys is refused, because neither can be honoured in an account the caller does
// not own.
func LaunchReady(spec *CreateMachineSpec) error {
	o, ok := offerFor(spec.InstanceType)
	if !ok {
		return fmt.Errorf("unknown size: %s", spec.InstanceType)
	}
	if spec.ImageID != "" {
		return fmt.Errorf("hosted machines boot the configured image; imageId is not accepted")
	}
	if len(spec.SSHKeyIDs) > 0 {
		return fmt.Errorf("hosted machines take no provider SSH keys; sshKeyIds is not accepted")
	}
	acct := hostedConfig()
	if err := acct.launchable(o); err != nil {
		return err
	}
	if spec.Region != "" && spec.Region != acct.Region {
		return fmt.Errorf("region %s is not offered: hosted machines launch in %s", spec.Region, acct.Region)
	}
	return nil
}

// LaunchOrgMachine starts a hosted machine for org, attributed to project, and
// returns it. The scope tags are set here from the resolved org and project,
// never from the body, so the machine is always billed to the right tenant.
// Customer tags are not written to the instance; the tags on it are the ones the
// meter and the isolation filters read.
func LaunchOrgMachine(ctx context.Context, org, project string, spec *CreateMachineSpec) (*Machine, error) {
	if !validOrgSlug(org) || !validFilterValue(org) {
		return nil, fmt.Errorf("invalid org slug %q", org)
	}
	if !validProjectSlug(project) || (project != "" && !validFilterValue(project)) {
		return nil, fmt.Errorf("invalid project slug %q", project)
	}
	if err := LaunchReady(spec); err != nil {
		return nil, err
	}
	o, _ := offerFor(spec.InstanceType)
	acct := hostedConfig()
	api, region, err := hostedEC2(ctx)
	if err != nil {
		return nil, err
	}

	id := mintMachineID()
	tags := map[string]string{
		orgTagKey:     org,
		machineTagKey: id,
		managedByKey:  managedBy,
		kindTagKey:    CanonicalKind(spec.Tags[kindTagKey]),
	}
	if project != "" {
		tags[projectTagKey] = project
	}
	if app := strings.TrimSpace(spec.Tags[appTagKey]); app != "" && safeTagField(app) {
		tags[appTagKey] = app
	}
	// The bot bootstrap reads the org off the spec's tags; it gets its own copy so
	// a batch sharing one tag map is never written through.
	boot := *spec
	boot.Tags = maps.Clone(spec.Tags)
	if boot.Tags == nil {
		boot.Tags = map[string]string{}
	}
	boot.Tags[orgTagKey] = org

	inst, err := run(ctx, api, acct, launch{
		id:       id,
		offer:    o,
		name:     firstNonEmpty(spec.DisplayName, spec.Name, id),
		tags:     tags,
		userData: buildBotUserData(&boot),
	})
	if err != nil {
		return nil, err
	}
	machine := machineFromInstance(*inst, region)
	// A launched bot is an org member AND a playground node: register it as an
	// IAM agent-user (surfaces in hanzo.team) and plant it in the org playground
	// node registry attributed to org. Both best-effort — a registration failure
	// never fails the launch; each registry reconciles later.
	if specIsBot(spec) {
		registerBotUser(org, spec.Name, spec.DisplayName)
		registerPlaygroundNode(org, spec.Name)
	}
	return machine, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// ListMeteredMachines returns every running or stopped machine hosted compute
// launched, in every org — the set the hourly meter debits: a running machine
// its price, a stopped one its disk. The org is recovered per machine from its
// own tag, so ONE sweep meters every tenant. An instance with no machine id has
// no meter id either, so it is reported and left out rather than billed under an
// id another instance could share.
func ListMeteredMachines() ([]*Machine, error) {
	ctx, cancel := bounded()
	defer cancel()
	api, region, err := hostedEC2(ctx)
	if err != nil {
		return nil, err
	}
	found, err := describe(ctx, api,
		filter("tag:"+managedByKey, managedBy),
		filter("instance-state-name", string(ec2Types.InstanceStateNameRunning), string(ec2Types.InstanceStateNameStopped)))
	if err != nil {
		return nil, refusal("list metered machines", err, "", "")
	}
	machines := make([]*Machine, 0, len(found))
	for _, inst := range found {
		m := machineFromInstance(inst, region)
		if !machineIDPattern.MatchString(m.Id) {
			logs.Warning("compute metering: instance %s carries no machine id; not billed", aws.ToString(inst.InstanceId))
			continue
		}
		machines = append(machines, m)
	}
	return machines, nil
}
