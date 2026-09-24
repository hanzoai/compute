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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/util"
	"github.com/hanzoai/orm/relational/schemas"
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
	Region   string `xorm:"varchar(100)" json:"region"`

	State       string `xorm:"varchar(100)" json:"state"`
	ProviderUrl string `xorm:"varchar(200)" json:"providerUrl"`

	// Keys names ADDITIONAL accounts of the same cloud under one provider row, so
	// a launch can cycle across accounts without a row per account. The row's
	// own account (labelled by its Name) is index 0; these are 1..n. A key is a
	// LABEL in egress custody and nothing else: the credential it names is held
	// by egress, never here. A key carries its own liveness so one rate-limited
	// or revoked account is skipped without disabling the provider.
	Keys []ProviderKey `xorm:"mediumtext" json:"keys"`
}

// ProviderKey is one account in a provider's rotation.
type ProviderKey struct {
	// Name is the account's label in egress custody.
	Name string `json:"name"`
	// Region overrides the provider row's Region for launches on this key; empty
	// inherits the row's Region.
	Region string `json:"region"`
	// State is the key's own liveness. Empty or "active" means usable; anything
	// else (e.g. "error", "rate-limited", "revoked") takes it out of rotation
	// until an operator clears it, so one bad account never fails a launch that
	// another account could serve.
	State string `json:"state"`
}

// ErrKeyNotStored is a provider write that carries a cloud key. A key is
// enrolled in egress custody, where it is spent and never read back; a provider
// row names the account and holds nothing that spends.
var ErrKeyNotStored = errors.New("a provider row stores no cloud key: enrol the key in egress custody and name its label here")

// RefuseProviderKeys reports ErrKeyNotStored when a provider write's raw JSON
// carries a key — clientSecret, clientId, or a secret or keyId on any key — so
// a key sent here is refused, rather than dropped by the decoder and believed
// saved by whoever sent it.
func RefuseProviderKeys(body []byte) error {
	var raw struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		Keys         []struct {
			KeyID  string `json:"keyId"`
			Secret string `json:"secret"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	if strings.TrimSpace(raw.ClientID) != "" || strings.TrimSpace(raw.ClientSecret) != "" {
		return ErrKeyNotStored
	}
	for _, k := range raw.Keys {
		if strings.TrimSpace(k.KeyID) != "" || strings.TrimSpace(k.Secret) != "" {
			return ErrKeyNotStored
		}
	}
	return nil
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

func UpdateProvider(id string, provider *Provider) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, err := getProvider(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}

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
	engine, err := EngineFor(provider.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Insert(provider)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteProvider(provider *Provider) (bool, error) {
	engine, err := EngineFor(provider.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{provider.Owner, provider.Name}).Delete(&Provider{})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (provider *Provider) getId() string {
	return fmt.Sprintf("%s/%s", provider.Owner, provider.Name)
}

// LaunchCredential is one usable (account, region) a launch can run on. It flattens
// a provider's own credential and its rotation Keys into the uniform shape the
// selector cycles over, so the caller never reaches into either representation.
type LaunchCredential struct {
	KeyName string // the ProviderKey.Name, or "" for the provider's own account
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

// LaunchCredentials returns every usable account on a provider, in a stable
// order: the provider's own account (labelled by the row's Name) first, then each
// active key in declared order. A revoked or rate-limited key is omitted, so the
// caller cycles only over accounts that can actually serve a launch.
func (p *Provider) LaunchCredentials() []LaunchCredential {
	if p == nil {
		return nil
	}
	out := make([]LaunchCredential, 0, len(p.Keys)+1)
	out = append(out, LaunchCredential{KeyName: "", Region: p.Region})
	for _, k := range p.Keys {
		if !keyIsActive(k.State) || k.Name == "" {
			continue
		}
		region := k.Region
		if region == "" {
			region = p.Region
		}
		out = append(out, LaunchCredential{KeyName: k.Name, Region: region})
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
// Account, which selects the credential egress holds, so each account MUST carry
// a distinct label or a carried launch resolves them all to one key and the
// cycling has no effect. The row's own account keeps the provider's own label;
// an additional key uses its own name. It carries no key: there is none here.
//
// A row the SuperAdmin org does not own is a tenant's own account, and says so
// (Tenant), so it is never carried under compute's identity. Only the reserved
// admin org's rows are the platform's: any member of any other org may write a
// row in it, so no other org's rows can stand for the platform.
func (p *Provider) credential(c LaunchCredential) service.Credential {
	label := c.KeyName
	if label == "" {
		label = p.Name
	}
	tenant := p.Owner
	if service.IsSuperAdmin(p.Owner) {
		tenant = ""
	}
	return service.Credential{
		Provider: p.Type,
		Name:     label,
		Region:   c.Region,
		Tenant:   tenant,
	}
}
