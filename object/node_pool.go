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

package object

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/orm/relational/schemas"
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

type NodePool struct {
	Owner       string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name        string `xorm:"varchar(100) notnull pk" json:"name"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`

	ClusterID   string `xorm:"varchar(100)" json:"clusterId"`
	PoolID      string `xorm:"varchar(100)" json:"poolId"`
	Provider    string `xorm:"varchar(100)" json:"provider"`
	Size        string `xorm:"varchar(100)" json:"size"`
	Count       int    `json:"count"`
	MinNodes    int    `json:"minNodes"`
	MaxNodes    int    `json:"maxNodes"`
	AutoScale   bool   `json:"autoScale"`
	State       string `xorm:"varchar(100)" json:"state"`
	CostPerHour int64  `json:"costPerHour"` // cents
	OrgID       string `xorm:"varchar(100)" json:"orgId"`
	ProjectID   string `xorm:"varchar(100)" json:"projectId"`
}

func (pool *NodePool) GetId() string {
	return fmt.Sprintf("%s/%s", pool.Owner, pool.Name)
}

// computeEvent builds this node pool's analytics fleet event (kind=nodepool).
// org falls back to the pool's IAM owner when no explicit billing OrgID is
// set; the DOKS PoolID identifies the unit (its owner/name id when the pool is
// not yet provisioned), Size is its node slug, and priceCents is the caller's
// per-event price (CostPerHour on create/scale, 0 on destroy). The single
// pool→event mapping, so every emit path writes one consistent shape.
func (pool *NodePool) computeEvent(event string, priceCents int64) service.ComputeEvent {
	org := pool.OrgID
	if org == "" {
		org = pool.Owner
	}
	id := pool.PoolID
	if id == "" {
		id = pool.GetId()
	}
	return service.ComputeEvent{
		Org:        org,
		Project:    pool.ProjectID,
		Kind:       service.KindNodePool,
		Event:      event,
		MachineID:  id,
		Size:       pool.Size,
		PriceCents: priceCents,
	}
}

// GetAllNodePools fetches all Active node pools across ALL owners for billing
// reporting. Under Postgres this is one query over the single engine; under Base
// it unions the per-org SQLite DBs (allEngines), since no single table spans
// tenants.
func GetAllNodePools(pools *[]*NodePool) error {
	engines, err := allEngines()
	if err != nil {
		return err
	}
	for _, engine := range engines {
		batch := []*NodePool{}
		if err := engine.Where("state = ?", "Active").Find(&batch); err != nil {
			return err
		}
		*pools = append(*pools, batch...)
	}
	return nil
}

func GetNodePoolCount(owner, field, value string) (int64, error) {
	session := GetSession(owner, -1, -1, field, value, "", "")
	return session.Count(&NodePool{})
}

func GetNodePools(owner string) ([]*NodePool, error) {
	pools := []*NodePool{}
	engine, err := EngineFor(owner)
	if err != nil {
		return pools, err
	}
	err = engine.Desc("created_time").Find(&pools, &NodePool{Owner: owner})
	if err != nil {
		return pools, err
	}

	return pools, nil
}

func GetPaginationNodePools(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*NodePool, error) {
	pools := []*NodePool{}
	session := GetSession(owner, offset, limit, field, value, sortField, sortOrder)
	err := session.Find(&pools)
	if err != nil {
		return pools, err
	}

	return pools, nil
}

func getNodePool(owner string, name string) (*NodePool, error) {
	if owner == "" || name == "" {
		return nil, nil
	}

	engine, err := EngineFor(owner)
	if err != nil {
		return nil, err
	}
	pool := NodePool{Owner: owner, Name: name}
	existed, err := engine.Get(&pool)
	if err != nil {
		return &pool, err
	}

	if existed {
		return &pool, nil
	} else {
		return nil, nil
	}
}

func GetNodePool(id string) (*NodePool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	return getNodePool(owner, name)
}

// UpdateNodePool applies a client's edit to a node pool — and the row it writes
// is the STORED row with the editable fields applied, never the request body.
//
// The difference is the whole meter. The hourly sweep decides what to bill from
// State, Count and CostPerHour; the create path decides ownership from Owner and
// OrgID; the provider is identified by ClusterID and PoolID. Writing the body's
// columns handed all of those to the customer: `{"state":"Deleted"}` removed a
// running GPU pool from billing forever, `{"costPerHour":1}` re-priced it to a
// cent an hour, and a body naming another owner/name rewrote the primary key and
// corrupted a second row. The pool kept running either way — DigitalOcean never
// heard about any of it.
//
// So there are exactly three editable fields, and they are the autoscale bounds:
// nothing the meter reads is reachable from a request. Size, Count, State and
// CostPerHour are the PROVIDER's answer, written only by the paths that ask it
// (CreateNodePoolCloud, ScaleNodePoolCloud, SyncNodePoolsCloud, RecordSeedPool).
func UpdateNodePool(id string, pool *NodePool) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	stored, err := getNodePool(owner, name)
	if err != nil {
		return false, err
	} else if stored == nil {
		return false, nil
	}

	stored.MinNodes = pool.MinNodes
	stored.MaxNodes = pool.MaxNodes
	stored.AutoScale = pool.AutoScale
	stored.UpdatedTime = time.Now().Format(time.RFC3339)

	engine, err := EngineFor(owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{owner, name}).AllCols().Update(stored)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

// RecordSeedPool persists a cluster's seed pool as the row the hourly sweep
// bills. It is the object half of service's recordSeed seam, handed to the
// metered cluster provision at the composition root.
//
// The row is written with the rate the org was authorized at (CostPerHour) and
// CreatedTime NOW, which is what makes the hour exactly-once: the provision path
// debited this hour, and the sweep's one launch-hour rule (service.CreatedInHour)
// skips a pool created in the current hour. Hour two is the sweep's, and every
// hour after.
//
// Idempotent on (Owner, Name) FOR THE SAME CLUSTER: a re-recorded pool updates in
// place rather than failing the PK, so a retried create never leaves the cluster
// unbilled.
//
// A DIFFERENT cluster under the same (Owner, Name) is a COLLISION and is refused.
// The name is the customer's — it comes straight out of the cluster-create body —
// so two clusters asking for the same seed-pool name is a thing any tenant can do
// twice in a row, by accident or otherwise. Updating in place there repointed the
// first cluster's row at the second cluster, and the first cluster stopped being a
// row anybody could bill.
//
// The refusal is safe precisely because the row is no longer the meter of record
// for a house cluster: the hourly sweep bills the provider's live pools, so the
// second cluster is billed whether or not this row lands. What the refusal buys is
// that the FIRST cluster's row — its rate, its project, its create hour — is not
// silently overwritten by the second.
func RecordSeedPool(p service.SeedPool) error {
	if p.Org == "" || p.Name == "" {
		return fmt.Errorf("a billable pool needs an org and a name (got org=%q name=%q)", p.Org, p.Name)
	}
	now := time.Now().Format(time.RFC3339)
	pool := &NodePool{
		Owner:       p.Org,
		Name:        p.Name,
		ClusterID:   p.ClusterID,
		Size:        p.Size,
		Count:       p.Count,
		State:       "Active",
		CostPerHour: p.CentsHour,
		OrgID:       p.Org,
		ProjectID:   p.Project,
		CreatedTime: now,
		UpdatedTime: now,
	}
	existing, err := getNodePool(p.Org, p.Name)
	if err != nil {
		return err
	}
	engine, err := EngineFor(p.Org)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.ClusterID != "" && existing.ClusterID != p.ClusterID {
			return fmt.Errorf("a billable pool row for %s/%s already belongs to cluster %s; "+
				"refusing to repoint it at cluster %s", p.Org, p.Name, existing.ClusterID, p.ClusterID)
		}
		pool.CreatedTime = existing.CreatedTime
		_, err = engine.ID(schemas.PK{p.Org, p.Name}).AllCols().Update(pool)
		return err
	}
	_, err = engine.Insert(pool)
	return err
}

// ForgetClusterPools removes every billable row belonging to a cluster — the
// object half of service's forgetCluster seam. A row that outlives its cluster
// keeps invoicing nodes that no longer exist, so a teardown clears them.
func ForgetClusterPools(org, clusterID string) error {
	if org == "" || clusterID == "" {
		return fmt.Errorf("clearing a cluster's billable rows needs an org and a cluster id (got org=%q cluster=%q)", org, clusterID)
	}
	engine, err := EngineFor(org)
	if err != nil {
		return err
	}
	// Conditions come from the bean, not from hand-written column names: the ORM
	// owns the field→column mapping, and a name spelled here is a name that can
	// drift from the one the table actually has.
	_, err = engine.Delete(&NodePool{Owner: org, ClusterID: clusterID})
	return err
}

func AddNodePool(pool *NodePool) (bool, error) {
	engine, err := EngineFor(pool.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Insert(pool)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

func DeleteNodePool(pool *NodePool) (bool, error) {
	// Load the authoritative record first so a destroyed event carries the
	// pool's billing attribution + identity even when the caller supplied only
	// the PK (the delete controller unmarshals a sparse pool).
	full, _ := getNodePool(pool.Owner, pool.Name)

	engine, err := EngineFor(pool.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{pool.Owner, pool.Name}).Delete(&NodePool{})
	if err != nil {
		return false, err
	}

	if affected != 0 {
		emitPool := full
		if emitPool == nil {
			emitPool = pool
		}
		// Roll a destroyed event into the analytics datastore (best-effort).
		service.EmitComputeEvent(emitPool.computeEvent(service.ComputeDestroyed, 0))
	}

	return affected != 0, nil
}

// SyncNodePoolsCloud fetches node pools from DO for all active DigitalOcean providers
// with a ClusterID and upserts them into the DB.
func SyncNodePoolsCloud(owner string) (bool, error) {
	providers, err := getActiveCloudProviders(owner)
	if err != nil {
		return false, err
	}

	synced := false
	for _, provider := range providers {
		if provider.Type != "DigitalOcean" || provider.ClusterID == "" {
			continue
		}

		token := provider.ClientSecret
		if token == "" {
			token = provider.ClientId
		}

		client, err := service.NewDOKSClient(token, provider.ClusterID)
		if err != nil {
			return false, fmt.Errorf("failed to create DOKS client for provider %s: %w", provider.Name, err)
		}

		servicePools, err := client.ListNodePools()
		if err != nil {
			return false, fmt.Errorf("failed to list node pools from provider %s: %w", provider.Name, err)
		}

		for _, sp := range servicePools {
			dbPool, err := getNodePool(owner, sp.Name)
			if err != nil {
				return false, err
			}

			now := time.Now().Format(time.RFC3339)
			pool := &NodePool{
				Owner:       owner,
				Name:        sp.Name,
				ClusterID:   provider.ClusterID,
				PoolID:      sp.ID,
				Provider:    provider.Name,
				Size:        sp.Size,
				Count:       sp.Count,
				MinNodes:    sp.MinNodes,
				MaxNodes:    sp.MaxNodes,
				AutoScale:   sp.AutoScale,
				State:       "Active",
				UpdatedTime: now,
			}

			engine, err := EngineFor(owner)
			if err != nil {
				return false, err
			}
			if dbPool != nil {
				// Preserve billing attribution fields from DB
				pool.OrgID = dbPool.OrgID
				pool.ProjectID = dbPool.ProjectID
				pool.CostPerHour = dbPool.CostPerHour
				pool.CreatedTime = dbPool.CreatedTime
				_, err = engine.ID(schemas.PK{owner, sp.Name}).AllCols().Update(pool)
			} else {
				pool.CreatedTime = now
				_, err = engine.Insert(pool)
			}
			if err != nil {
				return false, err
			}
			synced = true
		}
	}

	return synced, nil
}

// CreateNodePoolCloud creates a new node pool in DOKS via the cloud provider and persists it.
//
// Money gate: the pool is priced from the resale catalog and the owner is
// authorized for its FULL first hour (hourly × node count) BEFORE anything is
// provisioned. A size that cannot be priced and an owner that cannot be
// authorized both provision nothing — the pool used to be created with no gate
// at all and then billed at CostPerHour, which was never computed here, so a
// GPU pool ran at $0/hr for as long as it stayed up.
func CreateNodePoolCloud(owner, providerName, clusterID string, spec *service.CreateNodePoolSpec) (*NodePool, error) {
	provider, err := getProvider(owner, providerName)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("provider %q not found for owner %q", providerName, owner)
	}

	token := provider.ClientSecret
	if token == "" {
		token = provider.ClientId
	}

	cid := clusterID
	if cid == "" {
		cid = provider.ClusterID
	}
	if cid == "" {
		return nil, fmt.Errorf("no cluster ID specified and provider %q has no default cluster", providerName)
	}

	// Price FIRST, from the ONE resale catalog: a size that cannot be priced
	// provisions nothing, so nothing is even built.
	rate, err := service.HourlyCents(spec.Size)
	if err != nil {
		return nil, err
	}
	client, err := service.NewDOKSClient(token, cid)
	if err != nil {
		return nil, fmt.Errorf("failed to create DOKS client: %w", err)
	}
	return createNodePoolMetered(client, rate, owner, provider.Project, providerName, cid, spec)
}

// cloudNodePoolCreator is the minimal cloud surface a metered node-pool provision
// needs — the twin of cloudNodePoolDeleter, satisfied by *service.DOKSClient and
// by a test fake. It is what makes "a refused pool reaches no upstream" a
// property a test can observe rather than a claim about a code path.
type cloudNodePoolCreator interface {
	CreateNodePool(spec *service.CreateNodePoolSpec) (*service.NodePool, error)
}

// createNodePoolMetered is the ONE metered node-pool provision: authorize the org
// for the pool's first hour at the quantity the pool is ALLOWED to reach,
// provision, persist the billable row, then debit that hour at the quantity it
// actually HAS — the whole sequence under the org's provisioning hold
// (service.Provision). rate is the per-node hourly price its caller resolved from
// the resale catalog.
//
// The two quantities are deliberately different for an autoscaling pool, and the
// gate takes both: authorizedNodes is the ceiling the balance must cover,
// poolNodes is the floored count the upstream is actually asked for and the only
// one the customer owes at hour one.
func createNodePoolMetered(client cloudNodePoolCreator, rate int64, owner, project, providerName, clusterID string, spec *service.CreateNodePoolSpec) (*NodePool, error) {
	// The count asked of the upstream is the count the org is DEBITED for. A
	// non-positive count used to reach DigitalOcean as-is while the gate priced a
	// floor of one, so "debited" and "provisioned" were two different numbers.
	spec.Count = poolNodes(spec)

	var pool *NodePool
	err := service.Provision(context.Background(), owner, project,
		rate*int64(authorizedNodes(spec)), rate*int64(spec.Count), spec.Size, func() (string, error) {
			servicePool, err := client.CreateNodePool(spec)
			if err != nil {
				return "", fmt.Errorf("failed to create node pool in DOKS: %w", err)
			}

			now := time.Now().Format(time.RFC3339)
			pool = &NodePool{
				Owner:       owner,
				Name:        servicePool.Name,
				ClusterID:   clusterID,
				PoolID:      servicePool.ID,
				Provider:    providerName,
				Size:        servicePool.Size,
				Count:       servicePool.Count,
				MinNodes:    servicePool.MinNodes,
				MaxNodes:    servicePool.MaxNodes,
				AutoScale:   servicePool.AutoScale,
				State:       "Active",
				CostPerHour: rate,
				OrgID:       owner,
				ProjectID:   project,
				CreatedTime: now,
				UpdatedTime: now,
			}
			if _, err := AddNodePool(pool); err != nil {
				return "", fmt.Errorf("node pool created in DOKS but DB insert failed: %w", err)
			}

			// Roll a launched event into the analytics datastore (best-effort; never
			// blocks or fails the create) — kind=nodepool for the Clusters board.
			service.EmitComputeEvent(pool.computeEvent(service.ComputeLaunched, pool.CostPerHour))
			return "pool-" + pool.PoolID, nil
		})
	if err != nil {
		return nil, err
	}
	return pool, nil
}

// poolNodes is how many nodes a pool spec provisions: at least one. The ONE
// count floor, read by the upstream request and by the gate alike.
func poolNodes(spec *service.CreateNodePoolSpec) int {
	if spec.Count < 1 {
		return 1
	}
	return spec.Count
}

// authorizedNodes is how many nodes a pool spec must be AUTHORIZED for: what it
// provisions, raised to the AUTOSCALE CEILING when the pool may grow on its own.
//
// The ceiling matters because nothing gates that growth. MinNodes/MaxNodes/
// AutoScale are forwarded to the upstream, which then adds nodes whenever the
// scheduler asks — no request reaches visor, so the money gate never runs. An org
// authorized for one node could have a pool that walks to its ceiling of sixteen
// on a balance that never covered them. Authorizing the ceiling up front is what
// makes "authorized" mean "can afford what this pool is allowed to become".
func authorizedNodes(spec *service.CreateNodePoolSpec) int {
	n := poolNodes(spec)
	if !spec.AutoScale {
		return n
	}
	for _, bound := range []int{spec.MinNodes, spec.MaxNodes} {
		if bound > n {
			n = bound
		}
	}
	return n
}

// ScaleNodePoolCloud updates the node count of an existing DOKS node pool.
//
// Money gate: scaling UP is a provision, so the owner is authorized for the
// first hour of the ADDED nodes before the upstream is touched. Scaling down (or
// to the same count) adds no cost and is never gated — refusing to shrink a pool
// because a balance is low would keep the meter running on nodes the customer
// asked to release.
func ScaleNodePoolCloud(owner, providerName, clusterID, poolID string, count int) (*NodePool, error) {
	provider, err := getProvider(owner, providerName)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("provider %q not found for owner %q", providerName, owner)
	}

	token := provider.ClientSecret
	if token == "" {
		token = provider.ClientId
	}

	cid := clusterID
	if cid == "" {
		cid = provider.ClusterID
	}

	client, err := service.NewDOKSClient(token, cid)
	if err != nil {
		return nil, fmt.Errorf("failed to create DOKS client: %w", err)
	}
	current, err := client.GetNodePool(poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get current node pool: %w", err)
	}
	// The pool's rate is resolved once, from the ONE resale catalog, and used for
	// two things: the authorization when the pool GROWS, and the re-stamp on the
	// row. An unresolvable rate is 0 here — HourlyCents never returns a zero price
	// with a nil error, so zero unambiguously means UNPRICED, and only the growing
	// path treats that as fatal.
	rate, _ := service.HourlyCents(current.Size)
	return scaleNodePoolMetered(client, current, rate, owner, provider.Project, poolID, count)
}

// cloudNodePoolScaler is the minimal cloud surface a metered scale WRITES to.
// Same seam pattern as cloudNodePoolCreator and cloudNodePoolDeleter, for the
// same reason — a gate is only proven by an upstream that stays untouched when it
// refuses.
type cloudNodePoolScaler interface {
	UpdateNodePool(poolID string, spec *service.CreateNodePoolSpec) (*service.NodePool, error)
}

// scaleNodePoolMetered is the ONE metered scale: a scale UP is a provision and
// goes through the gate under the org's hold, for the first hour of the ADDED
// nodes; a scale DOWN or a no-op adds no cost and is never gated. Refusing to
// shrink a pool because a balance is low — or because its slug has left the
// catalog — would keep the meter running on nodes the customer asked to release.
//
// rate is the per-node hourly price, or 0 when the slug has no price.
func scaleNodePoolMetered(client cloudNodePoolScaler, current *service.NodePool, rate int64, owner, project, poolID string, count int) (*NodePool, error) {
	spec := &service.CreateNodePoolSpec{
		Name:      current.Name,
		Size:      current.Size,
		Count:     count,
		MinNodes:  current.MinNodes,
		MaxNodes:  current.MaxNodes,
		AutoScale: current.AutoScale,
		Tags:      current.Tags,
		Labels:    current.Labels,
	}

	var updatedPool *service.NodePool
	scale := func() (string, error) {
		p, err := client.UpdateNodePool(poolID, spec)
		if err != nil {
			return "", fmt.Errorf("failed to scale node pool: %w", err)
		}
		updatedPool = p
		// No charge of its OWN: the hourly sweep bills the pool at its new count
		// from its next hour, and a debit here would overlap that same hour.
		return "", nil
	}

	var err error
	if added := count - current.Count; added > 0 {
		if rate <= 0 {
			// A scale UP is a provision: no price, no provision.
			return nil, fmt.Errorf("%w: size %q has no resale price", service.ErrPriceUnavailable, current.Size)
		}
		err = service.Provision(context.Background(), owner, project,
			rate*int64(added), rate*int64(added), current.Size, scale)
	} else {
		_, err = scale()
	}
	if err != nil {
		return nil, err
	}

	// Update DB record
	dbPool, err := getNodePool(owner, updatedPool.Name)
	if err != nil {
		return nil, err
	}
	if dbPool != nil {
		dbPool.Count = updatedPool.Count
		if rate > 0 {
			dbPool.CostPerHour = rate
		}
		dbPool.UpdatedTime = time.Now().Format(time.RFC3339)
		engine, err := EngineFor(owner)
		if err != nil {
			return nil, err
		}
		_, err = engine.ID(schemas.PK{owner, updatedPool.Name}).AllCols().Update(dbPool)
		if err != nil {
			return nil, err
		}
		// Roll a running event into the analytics datastore at the pool's new
		// scale (best-effort) — kind=nodepool, price from CostPerHour.
		service.EmitComputeEvent(dbPool.computeEvent(service.ComputeRunning, dbPool.CostPerHour))
		return dbPool, nil
	}

	return nil, fmt.Errorf("node pool %q not found in DB", updatedPool.Name)
}

// cloudNodePoolDeleter is the minimal cloud-client surface needed to delete a
// node pool. It is satisfied by *service.DOKSClient and by test fakes.
type cloudNodePoolDeleter interface {
	DeleteNodePool(poolID string) error
}

// confirmCloudPoolDeleted asks the provider to delete the pool and reports
// whether it is now gone. A nil error means it was deleted; a provider 404 means
// it was already gone. Any other error (e.g. a 422 while the pool is still
// provisioning) is returned so the caller can retry without dropping the DB row.
func confirmCloudPoolDeleted(client cloudNodePoolDeleter, poolID string) error {
	err := client.DeleteNodePool(poolID)
	if err == nil || service.IsNotFound(err) {
		return nil
	}
	return err
}

// poolCloudClient builds the cloud client that can delete a STORED pool: the
// per-org Provider named on the row, or Hanzo's house account when the row names
// none (a cluster's seed pool is recorded by the house cluster create, which has
// no Provider row to name).
//
// A Provider the row names but the store does not have is an ERROR, never a skip.
// Skipping is how a row gets dropped while its pool keeps running.
func poolCloudClient(stored *NodePool) (cloudNodePoolDeleter, error) {
	if stored.Provider == "" {
		return service.NewHouseDOKSClient(stored.ClusterID)
	}
	provider, err := getProvider(stored.Owner, stored.Provider)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("node pool %s names provider %q, which does not exist for owner %q: "+
			"refusing to drop a billable row without confirming the pool is gone upstream",
			stored.GetId(), stored.Provider, stored.Owner)
	}
	token := provider.ClientSecret
	if token == "" {
		token = provider.ClientId
	}
	return service.NewDOKSClient(token, stored.ClusterID)
}

// DeleteNodePoolCloud deletes a node pool from its cloud provider and, only once
// the provider confirms the pool is gone, removes the DB row. A pool with no
// cloud linkage is a DB-only record and is removed directly. A provider error
// other than 404 (e.g. a transient 422 during provisioning) is propagated and the
// DB row is left intact so a retry can reconcile once the pool settles.
//
// EVERY field it acts on comes from the STORED ROW. The caller's pool argument
// says WHICH row (owner+name, and the owner is the authenticated org — the
// handler overwrites it); it never says which upstream pool that is, on whose
// account, in which cluster.
//
// It used to gate the whole upstream round-trip on the BODY's PoolID and Provider
// being non-empty. Omit either — send `{"name":"gpu"}` and nothing else — and
// visor skipped the provider entirely and deleted the row: the cluster kept
// running on Hanzo's house account and the row that billed it was gone. The body
// is the customer's, so opting out of the upstream check was one absent JSON field
// away. It also resolved the Provider from the body, and treated a Provider it
// could not find as permission to drop the row.
func DeleteNodePoolCloud(pool *NodePool) (bool, error) {
	stored, err := getNodePool(pool.Owner, pool.Name)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil // no such row in this org; nothing to delete
	}
	// Linkage decides whether there is an upstream pool at all, and it is read from
	// the row. A row carrying both is a pool that exists somewhere, and it is not
	// dropped until that somewhere says it is gone.
	if stored.PoolID != "" && stored.ClusterID != "" {
		client, err := poolCloudClient(stored)
		if err != nil {
			return false, err
		}
		if err := confirmCloudPoolDeleted(client, stored.PoolID); err != nil {
			return false, err
		}
	}

	return DeleteNodePool(stored)
}
