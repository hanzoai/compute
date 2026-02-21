// Copyright 2025 The casbin Authors. All Rights Reserved.
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
	"time"

	"github.com/casvisor/casvisor/service"
	"github.com/casvisor/casvisor/util"
	"xorm.io/core"
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

// GetAllNodePools fetches all node pools across all owners (for billing reporting).
func GetAllNodePools(pools *[]*NodePool) error {
	return adapter.engine.Where("state = ?", "Active").Find(pools)
}

func GetNodePoolCount(owner, field, value string) (int64, error) {
	session := GetSession(owner, -1, -1, field, value, "", "")
	return session.Count(&NodePool{})
}

func GetNodePools(owner string) ([]*NodePool, error) {
	pools := []*NodePool{}
	err := adapter.engine.Desc("created_time").Find(&pools, &NodePool{Owner: owner})
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

	pool := NodePool{Owner: owner, Name: name}
	existed, err := adapter.engine.Get(&pool)
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

func UpdateNodePool(id string, pool *NodePool) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, err := getNodePool(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}

	pool.UpdatedTime = time.Now().Format(time.RFC3339)

	affected, err := adapter.engine.ID(core.PK{owner, name}).AllCols().Update(pool)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

func AddNodePool(pool *NodePool) (bool, error) {
	affected, err := adapter.engine.Insert(pool)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

func DeleteNodePool(pool *NodePool) (bool, error) {
	affected, err := adapter.engine.ID(core.PK{pool.Owner, pool.Name}).Delete(&NodePool{})
	if err != nil {
		return false, err
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

			if dbPool != nil {
				// Preserve billing attribution fields from DB
				pool.OrgID = dbPool.OrgID
				pool.ProjectID = dbPool.ProjectID
				pool.CostPerHour = dbPool.CostPerHour
				pool.CreatedTime = dbPool.CreatedTime
				_, err = adapter.engine.ID(core.PK{owner, sp.Name}).AllCols().Update(pool)
			} else {
				pool.CreatedTime = now
				_, err = adapter.engine.Insert(pool)
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

	client, err := service.NewDOKSClient(token, cid)
	if err != nil {
		return nil, fmt.Errorf("failed to create DOKS client: %w", err)
	}

	servicePool, err := client.CreateNodePool(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create node pool in DOKS: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	pool := &NodePool{
		Owner:       owner,
		Name:        servicePool.Name,
		ClusterID:   cid,
		PoolID:      servicePool.ID,
		Provider:    providerName,
		Size:        servicePool.Size,
		Count:       servicePool.Count,
		MinNodes:    servicePool.MinNodes,
		MaxNodes:    servicePool.MaxNodes,
		AutoScale:   servicePool.AutoScale,
		State:       "Active",
		CreatedTime: now,
		UpdatedTime: now,
	}

	_, err = AddNodePool(pool)
	if err != nil {
		return nil, fmt.Errorf("node pool created in DOKS but DB insert failed: %w", err)
	}

	return pool, nil
}

// ScaleNodePoolCloud updates the node count of an existing DOKS node pool.
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

	// Fetch current pool to preserve settings
	currentPool, err := client.GetNodePool(poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get current node pool: %w", err)
	}

	spec := &service.CreateNodePoolSpec{
		Name:      currentPool.Name,
		Size:      currentPool.Size,
		Count:     count,
		MinNodes:  currentPool.MinNodes,
		MaxNodes:  currentPool.MaxNodes,
		AutoScale: currentPool.AutoScale,
		Tags:      currentPool.Tags,
		Labels:    currentPool.Labels,
	}

	updatedPool, err := client.UpdateNodePool(poolID, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to scale node pool: %w", err)
	}

	// Update DB record
	dbPool, err := getNodePool(owner, updatedPool.Name)
	if err != nil {
		return nil, err
	}
	if dbPool != nil {
		dbPool.Count = updatedPool.Count
		dbPool.UpdatedTime = time.Now().Format(time.RFC3339)
		_, err = adapter.engine.ID(core.PK{owner, updatedPool.Name}).AllCols().Update(dbPool)
		if err != nil {
			return nil, err
		}
		return dbPool, nil
	}

	return nil, fmt.Errorf("node pool %q not found in DB", updatedPool.Name)
}
