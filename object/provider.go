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
	"sync"

	"github.com/hanzoai/orm/relational/schemas"
	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/util"
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

	ClientId string `xorm:"varchar(100)" json:"clientId"`
	// ClientSecret is a token on most clouds and a whole service-account JSON
	// (about 2 KB) on Google Cloud, so it is text and not varchar(100).
	ClientSecret string `xorm:"mediumtext" json:"clientSecret"`
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

	// Keys holds ADDITIONAL credentials for the same cloud account family under
	// one provider row, so a launch can cycle across accounts without a row per
	// key. The row's own (ClientId, ClientSecret) is key index 0; these are 1..n.
	// A key carries its own liveness so one rate-limited or revoked account is
	// skipped without disabling the provider. Additive and Sync2-safe: an empty
	// slice is a single-key provider, exactly as before.
	Keys []ProviderKey `xorm:"mediumtext" json:"keys"`
}

// ProviderKey is one credential in a provider's rotation.
type ProviderKey struct {
	// Name distinguishes keys within a provider for attribution and logs; it is
	// not the cloud account id.
	Name string `json:"name"`
	// KeyID/Secret are the account's credential. For DO, Secret is the token and
	// KeyID is left empty, matching how the row's own ClientId/ClientSecret map.
	KeyID  string `json:"keyId"`
	Secret string `json:"secret"`
	// Region overrides the provider row's Region for launches on this key; empty
	// inherits the row's Region.
	Region string `json:"region"`
	// State is the key's own liveness. Empty or "active" means usable; anything
	// else (e.g. "error", "rate-limited", "revoked") takes it out of rotation
	// until an operator clears it, so one bad account never fails a launch that
	// another account could serve.
	State string `json:"state"`
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
	// The rotation keys are credentials too, so they mask the same way the row's
	// own secret does — otherwise adding a key would leak it through every API
	// read that masks the primary one. A masked key still shows its name and
	// region so an operator can see the rotation without seeing the secrets.
	for i := range provider.Keys {
		if provider.Keys[i].Secret != "" {
			provider.Keys[i].Secret = "***"
		}
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

	// A masked read round-trips "***" back on save; restore each masked secret —
	// the row's own AND every rotation key's — from the stored row so a save never
	// writes the literal mask over a live credential. Then seal any genuinely new
	// value: a value already sealed or restored passes through untouched.
	restoreMaskedSecrets(provider, p)
	sealProviderSecrets(provider)

	engine, err := EngineFor(owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{owner, name}).AllCols().Update(provider)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

func AddProvider(provider *Provider) (bool, error) {
	// Seal the credential(s) before the row is written, so KMS holds the secret
	// and the row holds only a reference. A no-op when KMS is unconfigured.
	sealProviderSecrets(provider)

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
		// compute's fleet. Roll a launched event (best-effort) — kind=cluster.
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
	affected, err := engine.ID(schemas.PK{provider.Owner, provider.Name}).Delete(&Provider{})
	if err != nil {
		return false, err
	}

	if affected != 0 {
		p := full
		if p == nil {
			p = provider
		}
		if p.isManagedCluster() {
			// The managed cluster is leaving compute's fleet. Roll a destroyed
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
// provider registers (kind=cluster). org is the provider's IAM owner; the
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

// LaunchCredential is one usable (account, region) a launch can run on. It flattens
// a provider's own credential and its rotation Keys into the uniform shape the
// selector cycles over, so the caller never reaches into either representation.
type LaunchCredential struct {
	KeyName string // the ProviderKey.Name, or "" for the provider's own credential
	KeyID   string
	Secret  string
	Region  string
}

// keyIsActive reports whether a rotation key's state permits use. Empty and
// "active" are both usable; anything else ("error", "rate-limited", "revoked")
// is out of rotation until an operator clears it. The default is USABLE so an
// operator who sets no state gets a working key rather than a silently-skipped
// one. This governs ProviderKey liveness only — the provider row's own lifecycle
// State is a separate vocabulary owned by isActiveCloudProvider.
func keyIsActive(state string) bool {
	return state == "" || state == "active"
}

// LaunchCredentials returns every usable credential on a provider, in a stable
// order: the provider's own (ClientId, ClientSecret) first, then each active key
// in declared order. A revoked or rate-limited key is omitted, so the caller
// cycles only over accounts that can actually serve a launch.
//
// The provider's own credential leads because it is the one that already ran the
// fleet; adding Keys must never demote it. A provider with no usable credential
// at all returns an empty slice, and the caller must treat that as "cannot
// launch here" rather than launching on a zero-value credential.
func (p *Provider) LaunchCredentials() []LaunchCredential {
	if p == nil {
		return nil
	}
	out := make([]LaunchCredential, 0, len(p.Keys)+1)
	// The row's own credential is key 0 whenever the row carries one. Its
	// lifecycle is the provider-level State, enforced where providers are
	// selected (isActiveCloudProvider) — this does not re-judge it in the key
	// vocabulary. A multi-account provider that wants every account independently
	// skippable leaves the row credential empty and lists all accounts as Keys.
	if p.ClientSecret != "" || p.ClientId != "" {
		out = append(out, LaunchCredential{
			KeyName: "",
			KeyID:   p.ClientId,
			// Resolve the secret from KMS here, at the one chokepoint every launch
			// and every credential read passes through, so the plaintext lives in
			// this slice for the length of a call and never in the row. A legacy
			// plaintext row resolves to itself (dual-read).
			Secret: openSecret(p.ClientSecret),
			Region: p.Region,
		})
	}
	for _, k := range p.Keys {
		if !keyIsActive(k.State) {
			continue
		}
		if k.Secret == "" && k.KeyID == "" {
			continue
		}
		region := k.Region
		if region == "" {
			region = p.Region
		}
		out = append(out, LaunchCredential{
			KeyName: k.Name,
			KeyID:   k.KeyID,
			Secret:  openSecret(k.Secret),
			Region:  region,
		})
	}
	return out
}

// Launch account selection.
//
// A provider's usable accounts (LaunchCredentials) are cycled round-robin so a
// burst of launches spreads across them instead of hammering one account into
// its rate limit. The cursor is per-provider: interleaved launches on different
// providers must not stride one provider's cursor by the count of the others, or
// a shared factor would pin it to a single account forever.
var (
	launchCursorMu sync.Mutex
	launchCursors  = map[string]uint64{}
)

func nextLaunchCursor(providerID string) uint64 {
	launchCursorMu.Lock()
	defer launchCursorMu.Unlock()
	c := launchCursors[providerID]
	launchCursors[providerID] = c + 1
	return c
}

// pickLaunchCredential chooses one usable credential for a launch by stepping a
// cursor over the account set. Any monotonically advancing cursor gives
// round-robin; an empty set has no launchable account and returns ok=false so
// the caller refuses the launch rather than running on a zero credential.
func pickLaunchCredential(creds []LaunchCredential, cursor uint64) (LaunchCredential, bool) {
	if len(creds) == 0 {
		return LaunchCredential{}, false
	}
	return creds[cursor%uint64(len(creds))], true
}

// LaunchCredentialFor picks the next account a launch should run on, cycling
// across this provider's usable accounts. ok=false means the provider has no
// usable account and the caller must not launch.
func (p *Provider) LaunchCredentialFor() (LaunchCredential, bool) {
	creds := p.LaunchCredentials()
	return pickLaunchCredential(creds, nextLaunchCursor(p.getId()))
}

// launchCredentialNamed resolves the account a launched machine recorded (its
// key name; "" is the provider's own account) back to a usable credential. A
// machine must be managed on the same account it launched on — on most clouds a
// resource created under one account's key is invisible to another's. ok=false
// means that account is gone or disabled and the machine can no longer be
// reached through it.
func (p *Provider) launchCredentialNamed(account string) (LaunchCredential, bool) {
	for _, c := range p.LaunchCredentials() {
		if c.KeyName == account {
			return c, true
		}
	}
	return LaunchCredential{}, false
}

// credential builds the cloud credential for one of this provider's accounts.
//
// Name is the account's egress label — the carrier passes it as the spend
// Account, which selects the credential KMS holds, so each account MUST carry a
// distinct label or a carried launch resolves them all to one KMS key and the
// cycling has no effect. The row's own account keeps the provider's own label
// (unchanged from the single-account past); an additional key uses its own name.
// KeyID/Secret are consulted only on the carrier-less path, where compute holds
// the token itself; under the carrier they are empty and egress attaches the key.
func (p *Provider) credential(c LaunchCredential) service.Credential {
	label := c.KeyName
	if label == "" {
		label = p.Name
	}
	return service.Credential{
		Provider: p.Type,
		Name:     label,
		KeyID:    c.KeyID,
		Secret:   c.Secret,
		Region:   c.Region,
	}
}
