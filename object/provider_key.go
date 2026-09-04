// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/util"
)

// AddProviderKey appends a rotation key to a provider, sealing its secret. The
// key's Name must be unique within the provider — it is the label a launch and
// the ledger attribute an account by, and two keys sharing it would resolve to
// one credential.
func AddProviderKey(id string, key ProviderKey) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, err := getProvider(owner, name)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, fmt.Errorf("provider %s not found", id)
	}
	if key.Name == "" {
		return false, fmt.Errorf("key name is required")
	}
	if keyIndex(p, key.Name) >= 0 {
		return false, fmt.Errorf("key %q already exists on provider %s", key.Name, id)
	}
	key.Secret = sealSecret(providerSecretKey(owner, name, "keys/"+key.Name), key.Secret)
	p.Keys = append(p.Keys, key)
	return saveProvider(p)
}

// RotateProviderKey rotates a rotation key's secret and/or sets its state
// (active|revoked|…). A real new secret is sealed and replaces the stored one; a
// masked or empty secret leaves the stored one intact, so a state change alone
// never clobbers the credential. KeyID and Region are updated when supplied.
func RotateProviderKey(id, keyName string, in ProviderKey) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, err := getProvider(owner, name)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, fmt.Errorf("provider %s not found", id)
	}
	i := keyIndex(p, keyName)
	if i < 0 {
		return false, fmt.Errorf("key %q not found on provider %s", keyName, id)
	}
	if in.Secret != "" && in.Secret != "***" {
		p.Keys[i].Secret = sealSecret(providerSecretKey(owner, name, "keys/"+keyName), in.Secret)
	}
	if in.KeyID != "" {
		p.Keys[i].KeyID = in.KeyID
	}
	if in.Region != "" {
		p.Keys[i].Region = in.Region
	}
	if in.State != "" {
		p.Keys[i].State = in.State
	}
	return saveProvider(p)
}

// DeleteProviderKey removes a rotation key from a provider. Idempotent: an
// already-absent key is a no-op success. The sealed blob is left in KMS —
// nothing references it once the row drops it, and a delete-through would add a
// second failure mode to a row edit for no gain.
func DeleteProviderKey(id, keyName string) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, err := getProvider(owner, name)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, fmt.Errorf("provider %s not found", id)
	}
	i := keyIndex(p, keyName)
	if i < 0 {
		return false, nil
	}
	p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
	return saveProvider(p)
}

// VerifyProvider dry-run validates a provider's stored credential: it resolves
// the secret, builds the cloud client, and does one cheap read (GetMachines). It
// creates nothing. account selects which credential to test — "" is the row's
// own, a name is a rotation key. ok reports whether the read succeeded; detail
// carries the cloud's error message when it did not, so a caller can show "this
// key works" or exactly why it does not.
func VerifyProvider(id, account string) (ok bool, detail string, err error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	p, e := getProvider(owner, name)
	if e != nil {
		return false, "", e
	}
	if p == nil {
		return false, "", fmt.Errorf("provider %s not found", id)
	}
	cred, found := p.launchCredentialNamed(account)
	if !found {
		return false, "", fmt.Errorf("provider %s has no usable credential %q", id, account)
	}
	client, e := service.NewMachineClient(p.credential(cred))
	if e != nil {
		return false, e.Error(), nil
	}
	if _, e := client.GetMachines(); e != nil {
		return false, e.Error(), nil
	}
	return true, "ok", nil
}
