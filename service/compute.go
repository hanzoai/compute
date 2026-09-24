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
// launches for an org in Hanzo's own EC2 account (ec2.go) and bills to that org,
// and the clusters of the platform Kubernetes accounts (kubernetes.go). It is
// distinct from the per-org "bring your own cloud" Provider path
// (object/machine_cloud.go), where an org's machines run on its own credentials.
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

// ---- platform Kubernetes accounts ----

// ListOrgKubernetesNodes returns one Machine per worker node of every platform
// cluster tagged for org, across every platform account. An account that cannot
// answer fails the read rather than silently shrinking it.
func ListOrgKubernetesNodes(org string) ([]*Machine, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	ctx := context.Background()
	clients, err := platformClients()
	if err != nil {
		return nil, err
	}
	tag := orgTag(org)
	var out []*Machine
	for _, c := range clients {
		clusters, err := c.ListClusters(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Provider(), err)
		}
		for _, k := range clusters {
			if !clusterHasTag(k.Tags, tag) {
				continue
			}
			detail, err := c.GetCluster(ctx, k.ID)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.Provider(), err)
			}
			out = append(out, detail.Nodes...)
		}
	}
	return out, nil
}

// platformClients is every platform Kubernetes account, or an error naming the
// first one that could not be built.
func platformClients() ([]KubernetesClientInterface, error) {
	clients, status := kubernetesClients()
	for _, s := range status {
		if !s.OK {
			return nil, fmt.Errorf("platform account %s unavailable: %s", s.Provider, s.Reason)
		}
	}
	return clients, nil
}

// unwrap is the provider client under the account that names it.
func unwrap(c KubernetesClientInterface) KubernetesClientInterface {
	if a, ok := c.(account); ok {
		return a.KubernetesClientInterface
	}
	return c
}

// LivePool is ONE node pool in a platform account as the PROVIDER reports it
// right now — which cluster it belongs to, which org owns that cluster, its node
// slug, and how many nodes it is ACTUALLY running.
//
// This is the billable unit of a platform cluster, and the provider is its author.
// The stored row is a cache of it: useful for the rate the org was authorized at
// and the project it belongs to, and authoritative for neither existence nor
// count.
type LivePool struct {
	// Org owns the cluster, recovered from its hanzo-org tag. Empty means the
	// cluster is unattributable and nothing may be billed for it.
	Org string
	// ClusterID and PoolID are the upstream identity — the pair no customer can
	// rename, delete, or collide with another tenant's.
	ClusterID string
	PoolID    string
	// Name is the pool's upstream name. It addresses the cached row; it never
	// identifies the pool.
	Name string
	Size string
	// Nodes is the LIVE node count, so an autoscaled pool bills what it grew to.
	Nodes int
	// Created is the owning cluster's creation time (RFC3339), the fallback
	// launch-hour marker for a pool with no stored row.
	Created string
}

// livePooler is a platform account that reports the node pools it runs.
type livePooler interface {
	LivePools(ctx context.Context) ([]LivePool, error)
}

// ListLivePools returns every node pool of every cluster in every platform
// account, with the org that owns it and its LIVE node count — the authoritative
// answer to "what is Hanzo running, for whom, and how much of it".
//
// It is all or nothing: the pool sweep bills from this answer, and a pool on an
// account that did not answer cannot be told from a pool that is gone.
func ListLivePools(ctx context.Context) ([]LivePool, error) {
	clients, err := platformClients()
	if err != nil {
		return nil, err
	}
	var out []LivePool
	for _, c := range clients {
		lp, ok := unwrap(c).(livePooler)
		if !ok {
			return nil, fmt.Errorf("platform account %s cannot report its node pools", c.Provider())
		}
		pools, err := lp.LivePools(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Provider(), err)
		}
		out = append(out, pools...)
	}
	return out, nil
}

// clusterScoped is a platform account that can act on one cluster's pools.
type clusterScoped interface {
	InCluster(clusterID string) *DOKSClient
}

// PlatformPools is the node-pool surface of one platform cluster, on whichever
// platform account holds it. A cluster's seed pool is recorded with no Provider
// row, and this is how it is reached.
type PlatformPools struct{ ClusterID string }

// DeleteNodePool deletes one pool of the cluster. A cluster no platform account
// has is gone, and so are its pools. An account that cannot answer is an error,
// never taken for absence, so a billable row is not dropped on a guess.
func (p PlatformPools) DeleteNodePool(poolID string) error {
	ctx := context.Background()
	clients, err := platformClients()
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		return fmt.Errorf("no platform account is configured: cannot confirm pool %s of cluster %s is gone", poolID, p.ClusterID)
	}
	for _, c := range clients {
		if _, err := c.GetCluster(ctx, p.ClusterID); err != nil {
			if IsNotFound(err) {
				continue
			}
			return fmt.Errorf("%s: %w", c.Provider(), err)
		}
		scoped, ok := unwrap(c).(clusterScoped)
		if !ok {
			return fmt.Errorf("platform account %s cannot act on node pools", c.Provider())
		}
		return scoped.InCluster(p.ClusterID).DeleteNodePool(poolID)
	}
	return nil
}

// orgFromClusterTags recovers the owning org from a cluster's tag LIST, through
// the SAME hanzo-org read-back the machine path uses on its comma-joined string
// form. One parser, so a cluster and a machine can never disagree about who owns
// them.
func orgFromClusterTags(tags []string) string {
	return orgFromTag(strings.Join(tags, ","))
}

// ListOrgKubernetesClusters returns every platform cluster tagged for org. Per-org
// isolation is by the cluster's hanzo-org tag: a tenant only ever sees its own
// clusters, never another org's.
func ListOrgKubernetesClusters(org string) ([]*KubernetesCluster, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	all, failed, err := listClustersAcross(context.Background())
	if err != nil {
		return nil, err
	}
	tag := orgTag(org)
	out := make([]*KubernetesCluster, 0, len(all))
	for _, k := range all {
		if clusterHasTag(k.Tags, tag) {
			out = append(out, k)
		}
	}
	// A cloud that did not answer costs its own rows, never the fleet. The list
	// cannot carry that (callers decode an array), so it is on GET /v1/k8s/providers.
	if len(failed) > 0 {
		logs.Warning("cloud providers degraded for org %s: %s", org, strings.Join(failed, "; "))
	}
	return out, nil
}

// GetOrgKubernetesCluster returns one platform cluster's detail (pools + worker nodes),
// but ONLY if it carries the caller org's hanzo-org tag. A cluster owned by another
// org — or a missing cluster — resolves to (nil, nil): the controller renders it as
// "not found", so a tenant can never read another tenant's cluster by guessing an id.
func GetOrgKubernetesCluster(org, id string) (*KubernetesClusterDetail, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	_, detail, err := findCluster(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, nil
	}
	if !clusterHasTag(detail.Tags, orgTag(org)) {
		return nil, nil // isolation: not this org's cluster
	}
	return detail, nil
}

// clusterCreator is the minimal cloud surface a metered cluster create needs. It
// is satisfied by *DOKSClient and by a test fake, which is what makes "refused
// requests provision NOTHING" a property a test can observe rather than a claim.
type clusterCreator interface {
	CreateCluster(ctx context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error)
}

// SeedPool is the node pool a cluster create provisions, in the terms the store
// needs to make it BILLABLE. A cluster's nodes are the cluster's whole cost, and
// the hourly sweep bills node-pool rows — so a cluster that provisions a pool and
// writes no row is billed its first hour by the provision path and then runs free
// forever. That is what this type exists to prevent.
type SeedPool struct {
	Org       string
	Project   string
	ClusterID string
	Name      string
	Size      string
	Count     int
	CentsHour int64
}

// recordSeed / forgetCluster are the node-pool store as the cluster provision path
// sees it: where a metered create writes its seed pool, and where a teardown
// clears what it wrote. They are plain functions because each call site depends on
// exactly one of them, and they are PARAMETERS because service cannot import
// object (object imports service) — handing the store in at the composition root
// is also what lets a test fake observe "a create wrote its billable row" and
// "a delete stopped the meter" instead of taking either on faith.
type recordSeed func(SeedPool) error

type forgetCluster func(org, clusterID string) error

// CreateOrgKubernetesCluster provisions a cluster on a platform account for org,
// stamping it managed-by + hanzo-org:<org> so it associates to the tenant — which
// is what makes it visible to that org's cluster and node listers (and invisible
// to every other org).
//
// A platform account means Hanzo pays the upstream bill for every node in the
// seed pool, so this goes through the money gate exactly like a machine launch —
// and records the pool it provisioned, so the sweep keeps billing it.
func CreateOrgKubernetesCluster(org, project string, spec *CreateClusterSpec, record recordSeed) (*KubernetesCluster, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	if spec == nil {
		return nil, fmt.Errorf("spec is required")
	}
	client, err := backendFor(spec.Provider)
	if err != nil {
		return nil, err
	}
	kc, err := createClusterMetered(context.Background(), client, record, org, project, spec)
	if err != nil {
		return nil, err
	}
	if kc != nil && kc.Provider == "" {
		kc.Provider = client.Provider()
	}
	return kc, nil
}

// createClusterMetered is the ONE metered cluster provision: price the seed pool
// from the catalog, authorize the org for its first hour at that price,
// provision, PERSIST the pool as a billable row, then record the first hour.
// Fail-closed on the balance AND on the price — an org that cannot be authorized
// and a size that cannot be priced both provision nothing.
//
// The charge is the seed pool's FULL first hour (hourly × node count), read from
// the same seedPoolCount the upstream request is built with, so the quantity
// authorized is the quantity provisioned.
//
// Hour one is this function's. EVERY HOUR AFTER IS THE SWEEP'S, and the sweep
// reads node-pool rows — so the row is not bookkeeping, it IS the recurring bill.
// The row is written BEFORE the debit, because a debit with no row is a cluster
// that bills once, and a row with no debit is one reconciled hour.
func createClusterMetered(ctx context.Context, client clusterCreator, record recordSeed, org, project string, spec *CreateClusterSpec) (*KubernetesCluster, error) {
	count := seedPoolCount(spec)
	hourly, err := HourlyCents(spec.NodePool.Size)
	if err != nil {
		return nil, err
	}
	var cluster *KubernetesCluster
	// A cluster's seed pool does not autoscale — CreateClusterSpec carries no
	// bounds — so the ceiling and the charge are the same count.
	err = Provision(ctx, org, project, hourly*int64(count), hourly*int64(count), spec.NodePool.Size, func() (string, error) {
		c, err := client.CreateCluster(ctx, spec, []string{"managed-by:hanzo-visor", orgTag(org)})
		if err != nil {
			return "", err
		}
		cluster = c
		if err := record(SeedPool{
			Org: org, Project: project, ClusterID: c.ID, Name: seedPoolName(spec),
			Size: spec.NodePool.Size, Count: count, CentsHour: hourly,
		}); err != nil {
			// The cluster is up and drawing upstream cost; refusing to return it
			// would leave the customer paying for something they were told they did
			// not get. But an unrecorded pool is an UNBILLED pool for every hour it
			// runs, so this is the loudest line in the file. Nothing reconciles it.
			logs.Warning("compute metering: cluster %s (org %s) provisioned but its seed pool was NOT recorded — it will be billed for its first hour only: %v", c.ID, org, err)
		}
		return "cluster-" + c.ID, nil
	}, nil)
	if err != nil {
		return nil, err
	}
	return cluster, nil
}

// DeleteOrgKubernetesCluster destroys a platform cluster by id, but ONLY if it carries
// the caller org's hanzo-org tag — the same isolation as GetOrgKubernetesCluster, so
// a tenant can never delete another tenant's cluster. An already-absent cluster is a
// no-op success (idempotent delete).
//
// The cluster's billable rows go with it. They are what the hourly sweep bills, so
// a row outliving its cluster is not stale data — it is an invoice for nodes that
// no longer exist. The meter is stopped even for an already-absent cluster, so a
// retry after a partial delete still closes the bill.
func DeleteOrgKubernetesCluster(org, id string, forget forgetCluster) error {
	if org == "" {
		return fmt.Errorf("org is required")
	}
	ctx := context.Background()
	client, detail, err := findCluster(ctx, id)
	if err != nil {
		return err
	}
	if detail == nil {
		// Gone on every backend. Stop billing it, same as the metered path does
		// for an upstream 404 — a cluster that no cloud has must not keep metering.
		stopClusterMeter(forget, org, id)
		return nil
	}
	return deleteClusterMetered(ctx, client, forget, org, id)
}

// clusterDestroyer is the minimal cloud surface a metered cluster teardown needs
// — the mirror of clusterCreator, and satisfied by the same *DOKSClient, so
// "a delete stops the meter" is observable against a fake exactly like
// "a refused create provisions nothing" is.
type clusterDestroyer interface {
	GetCluster(ctx context.Context, id string) (*KubernetesClusterDetail, error)
	DeleteCluster(ctx context.Context, id string) error
}

// deleteClusterMetered is the ONE metered cluster teardown: verify the cluster is
// this org's, destroy it, then stop its meter. A cluster already absent upstream
// still has its meter stopped, so a retry after a partial delete closes the bill.
//
// Clearing the rows is not tidiness. The hourly sweep bills node-pool rows, so a
// row that outlives its cluster is an invoice for nodes that no longer exist.
func deleteClusterMetered(ctx context.Context, client clusterDestroyer, forget forgetCluster, org, id string) error {
	detail, err := client.GetCluster(ctx, id)
	if err != nil {
		if IsNotFound(err) {
			stopClusterMeter(forget, org, id) // already gone upstream; stop billing it
			return nil
		}
		return err
	}
	if !clusterHasTag(detail.Tags, orgTag(org)) {
		return fmt.Errorf("cluster not found")
	}
	if err := client.DeleteCluster(ctx, id); err != nil {
		return err
	}
	stopClusterMeter(forget, org, id)
	return nil
}

// stopClusterMeter drops the cluster's billable rows. A failure is loud and never
// fails the delete — the cluster is gone either way, and the operator needs to
// know that a row is still metering nodes that no longer run.
func stopClusterMeter(forget forgetCluster, org, id string) {
	if err := forget(org, id); err != nil {
		logs.Warning("compute metering: cluster %s (org %s) deleted but its node-pool rows were NOT cleared — they will keep billing: %v", id, org, err)
	}
}
