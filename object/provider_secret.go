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
	"os"
	"strings"

	"github.com/hanzoai/orm/relational/schemas"
	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/object/kms"
)

// providerSecretKey is the KMS key a provider credential is sealed under. The
// leaf is "clientSecret" for the row's own credential and "keys/<name>" for a
// rotation key, so a rotation replaces exactly one leaf and never disturbs the
// others.
func providerSecretKey(owner, name, leaf string) string {
	return fmt.Sprintf("hanzo/%s/providers/%s/%s", owner, name, leaf)
}

// sealSecret stores a real plaintext secret in KMS and returns the reference to
// persist, or returns its input unchanged when there is nothing to seal (empty,
// the read-mask, or already a reference) or when KMS is not configured — in
// which case the plaintext column stays in force exactly as before KMS.
//
// A seal that fails keeps the plaintext rather than dropping the secret: a lost
// credential fails every launch on that account, so the safe failure is to keep
// the working value and log loudly for a later backfill.
func sealSecret(key, value string) string {
	if value == "" || value == "***" || kms.IsRef(value) {
		return value
	}
	if !kms.Configured() {
		return value
	}
	ref, err := kms.Seal(key, value)
	if err != nil {
		logs.Warning("provider secret: sealing %s failed, keeping plaintext: %v", key, err)
		return value
	}
	return ref.Encode()
}

// openSecret resolves a stored provider secret to plaintext: a KMS reference is
// fetched (and briefly cached in-process), anything else is already plaintext
// (dual-read, so a row not yet backfilled keeps working). A reference that fails
// to resolve returns "" and logs — the credential is then treated as unusable
// rather than handing a cloud API the literal reference string.
func openSecret(stored string) string {
	ref, ok := kms.Decode(stored)
	if !ok {
		return stored
	}
	v, err := kms.Open(ref)
	if err != nil {
		logs.Warning("provider secret: resolving %s failed: %v", ref.Path, err)
		return ""
	}
	return v
}

// sealProviderSecrets seals the row's own secret and every rotation key's secret
// in place, so what the store writes is references, not credentials. Idempotent:
// a value already sealed or masked passes through untouched.
func sealProviderSecrets(p *Provider) {
	p.ClientSecret = sealSecret(providerSecretKey(p.Owner, p.Name, "clientSecret"), p.ClientSecret)
	for i := range p.Keys {
		p.Keys[i].Secret = sealSecret(providerSecretKey(p.Owner, p.Name, "keys/"+p.Keys[i].Name), p.Keys[i].Secret)
	}
}

// restoreMaskedSecrets replaces masked incoming secrets with the ones already on
// file, so a console save of a masked read does not clobber live credentials
// with the literal "***". The row's own secret restores on the mask; a rotation
// key restores on the mask OR an empty secret (a key present in the save with no
// secret keeps the one on file), matched by Name. A genuinely new value passes
// through and is sealed by the caller.
func restoreMaskedSecrets(incoming, stored *Provider) {
	if incoming.ClientSecret == "***" {
		incoming.ClientSecret = stored.ClientSecret
	}
	for i := range incoming.Keys {
		if incoming.Keys[i].Secret != "***" && incoming.Keys[i].Secret != "" {
			continue
		}
		if j := keyIndex(stored, incoming.Keys[i].Name); j >= 0 {
			incoming.Keys[i].Secret = stored.Keys[j].Secret
		}
	}
}

// keyIndex returns the position of the named rotation key, or -1.
func keyIndex(p *Provider, name string) int {
	for i := range p.Keys {
		if p.Keys[i].Name == name {
			return i
		}
	}
	return -1
}

// saveProvider persists a mutated provider row across all columns.
func saveProvider(p *Provider) (bool, error) {
	engine, err := EngineFor(p.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID(schemas.PK{p.Owner, p.Name}).AllCols().Update(p)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

// ConfigureKMS wires the KMS vault from the environment and, when
// COMPUTE_KMS_BACKFILL is set, seals owner's existing plaintext provider secrets
// once. Dual-read (openSecret resolving either a reference or a plaintext) makes
// the backfill safe to run once and safe to leave un-run — a deployment with no
// KMS env keeps storing plaintext exactly as before.
func ConfigureKMS(owner string) {
	if err := kms.Configure(); err != nil {
		logs.Warning("kms: not configured, provider secrets stay plaintext: %v", err)
		return
	}
	if !kms.Configured() || strings.TrimSpace(os.Getenv("COMPUTE_KMS_BACKFILL")) == "" {
		return
	}
	n, err := BackfillProviderSecrets(owner)
	if err != nil {
		logs.Warning("kms backfill: %v", err)
		return
	}
	logs.Info("kms backfill: sealed %d provider secret(s) for %s", n, owner)
}

// BackfillProviderSecrets seals every still-plaintext provider secret for owner
// and rewrites the row to hold the reference. Safe to run repeatedly: a value
// already held as a reference is skipped. Returns the number of secrets sealed.
func BackfillProviderSecrets(owner string) (int, error) {
	if !kms.Configured() {
		return 0, nil
	}
	providers, err := GetProviders(owner)
	if err != nil {
		return 0, err
	}
	sealed := 0
	for _, p := range providers {
		changed := 0
		if p.ClientSecret != "" && !kms.IsRef(p.ClientSecret) {
			p.ClientSecret = sealSecret(providerSecretKey(p.Owner, p.Name, "clientSecret"), p.ClientSecret)
			changed++
		}
		for i := range p.Keys {
			if p.Keys[i].Secret != "" && !kms.IsRef(p.Keys[i].Secret) {
				p.Keys[i].Secret = sealSecret(providerSecretKey(p.Owner, p.Name, "keys/"+p.Keys[i].Name), p.Keys[i].Secret)
				changed++
			}
		}
		if changed == 0 {
			continue
		}
		if _, err := saveProvider(p); err != nil {
			return sealed, err
		}
		sealed += changed
	}
	return sealed, nil
}
