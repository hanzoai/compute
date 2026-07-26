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

	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

type Provider struct {
	Owner string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name  string `xorm:"varchar(100) notnull pk" json:"name"`
	// Project is the attribution dimension alongside Owner: the project WITHIN the
	// org that owns this BYOC provider. Additive, Sync2-safe, defaults to "".
	Project     string `xorm:"varchar(100)" json:"project"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`
	DisplayName string `xorm:"varchar(100)" json:"displayName"`

	Category string `xorm:"varchar(100)" json:"category"`
	Type     string `xorm:"varchar(100)" json:"type"`

	ClientId     string `xorm:"varchar(100)" json:"clientId"`
	ClientSecret string `xorm:"varchar(100)" json:"clientSecret"`
	Region       string `xorm:"varchar(100)" json:"region"`
	Network      string `xorm:"varchar(100)" json:"network"`
	Chain        string `xorm:"varchar(100)" json:"chain"`
	BrowserUrl   string `xorm:"varchar(200)" json:"browserUrl"`

	State       string `xorm:"varchar(100)" json:"state"`
	ProviderUrl string `xorm:"varchar(200)" json:"providerUrl"`

	ClusterID string `xorm:"varchar(100)" json:"clusterId"` // DOKS cluster UUID

	// CostReadScope carries the per-cloud identifier the fleet-billing cost collector
	// needs to read this BYOC account's spend, beyond the (ClientId, ClientSecret,
	// Region) triple used to manage machines. It is cloud-specific and additive
	// (Sync2-safe, defaults ""):
	//   AWS  — ignored (Cost Explorer is account-wide from the access key).
	//   DO   — ignored (the balance endpoint is account-wide from the token).
	//   Azure— "<tenantId>/<subscriptionId>" (Cost Management query scope + auth tenant).
	//   GCP  — "<project>.<dataset>.<table>" of the BigQuery billing-export table.
	// Empty means "cost-read not configured": the collector honestly skips this
	// provider (no fee) rather than fabricating spend.
	CostReadScope string `xorm:"varchar(300)" json:"costReadScope"`
}

func GetProviderCount(owner, field, value string) (int64, error) {
	session := GetSession(owner, -1, -1, field, value, "", "")
	return session.Count(&Provider{})
}

func GetProviders(owner string) ([]*Provider, error) {
	providers := []*Provider{}
	engine, err := EngineFor(owner)
	if err != nil {
		return providers, err
	}
	err = engine.Desc("created_time").Find(&providers, &Provider{Owner: owner})
	if err != nil {
		return providers, err
	}

	return providers, nil
}

func GetPaginationProviders(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Provider, error) {
	providers := []*Provider{}
	session := GetSession(owner, offset, limit, field, value, sortField, sortOrder)
	err := session.Find(&providers)
	if err != nil {
		return providers, err
	}

	return providers, nil
}

func getProvider(owner string, name string) (*Provider, error) {
	if owner == "" || name == "" {
		return nil, nil
	}

	engine, err := EngineFor(owner)
	if err != nil {
		return nil, err
	}
	provider := Provider{Owner: owner, Name: name}
	existed, err := engine.Get(&provider)
	if err != nil {
		return &provider, err
	}

	if existed {
		return &provider, nil
	} else {
		return nil, nil
	}
}

func GetProvider(id string) (*Provider, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	return getProvider(owner, name)
}

func GetMaskedProvider(provider *Provider, errs ...error) (*Provider, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}

	if provider == nil {
		return nil, nil
	}

	if provider.ClientSecret != "" {
		provider.ClientSecret = "***"
	}
	return provider, nil
}

func GetMaskedProviders(providers []*Provider, errs ...error) ([]*Provider, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}

	var err error
	for _, provider := range providers {
		provider, err = GetMaskedProvider(provider)
		if err != nil {
			return nil, err
		}
	}

	return providers, nil
}

func UpdateProvider(id string, provider *Provider) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, err := getProvider(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}

	if provider.ClientSecret == "***" {
		provider.ClientSecret = p.ClientSecret
	}

	engine, err := EngineFor(owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID([]any{owner, name}).AllCols().Update(provider)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

func AddProvider(provider *Provider) (bool, error) {
	engine, err := EngineFor(provider.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Insert(provider)
	if err != nil {
		return false, err
	}

	if affected != 0 && provider.isManagedCluster() {
		// A DOKS provider carrying a cluster UUID = a managed K8s cluster entering
		// visor's fleet. Roll a launched event (best-effort) — kind=cluster.
		service.EmitComputeEvent(provider.clusterEvent(service.ComputeLaunched))
	}

	return affected != 0, nil
}

func DeleteProvider(provider *Provider) (bool, error) {
	// Load the authoritative record first so a destroyed cluster event carries
	// the cluster UUID/region even when the caller supplied only the PK.
	full, _ := getProvider(provider.Owner, provider.Name)

	engine, err := EngineFor(provider.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID([]any{provider.Owner, provider.Name}).Delete(&Provider{})
	if err != nil {
		return false, err
	}

	if affected != 0 {
		p := full
		if p == nil {
			p = provider
		}
		if p.isManagedCluster() {
			// The managed cluster is leaving visor's fleet. Roll a destroyed
			// event (best-effort) — kind=cluster.
			service.EmitComputeEvent(p.clusterEvent(service.ComputeDestroyed))
		}
	}

	return affected != 0, nil
}

func (provider *Provider) getId() string {
	return fmt.Sprintf("%s/%s", provider.Owner, provider.Name)
}

// isManagedCluster reports whether this provider registers a real DOKS cluster —
// a DigitalOcean provider carrying a cluster UUID. Only such providers are a
// cluster in the compute-analytics sense; blockchain/other providers are not.
func (provider *Provider) isManagedCluster() bool {
	return provider.Type == "DigitalOcean" && provider.ClusterID != ""
}

// clusterEvent builds the analytics fleet event for the DOKS cluster this
// provider registers (kind=cluster). org is the provider's Casdoor owner; the
// cluster UUID identifies the unit and Region is its size lens. Price is 0 — the
// DOKS control plane is free; a cluster's compute cost lives in its node pools
// (kind=nodepool).
func (provider *Provider) clusterEvent(event string) service.ComputeEvent {
	return service.ComputeEvent{
		Org:       provider.Owner,
		Kind:      service.KindCluster,
		Event:     event,
		MachineID: provider.ClusterID,
		Size:      provider.Region,
	}
}
