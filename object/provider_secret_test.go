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
	"testing"

	"github.com/hanzoai/visor/object/kms"
)

// A save of a masked read must keep the rotation keys' secrets — the corruption
// this fixes wrote the literal "***" over each Keys[i].Secret because the restore
// only ever ran on the primary ClientSecret. The stored row's secrets survive the
// round-trip, matched by key name, and the primary is restored the same way.
func TestRestoreMaskedSecrets_KeepsRotationKeys(t *testing.T) {
	stored := &Provider{
		Owner: "acme", Name: "aws",
		ClientSecret: "primary-secret",
		Keys: []ProviderKey{
			{Name: "k1", KeyID: "AKIA1", Secret: "secret-one"},
			{Name: "k2", KeyID: "AKIA2", Secret: "secret-two"},
		},
	}
	// What a console sends back after reading the masked provider: every secret is
	// the mask, and one key's non-secret field (state) was edited.
	incoming := &Provider{
		Owner: "acme", Name: "aws",
		ClientSecret: "***",
		Keys: []ProviderKey{
			{Name: "k1", KeyID: "AKIA1", Secret: "***"},
			{Name: "k2", KeyID: "AKIA2", Secret: "***", State: "revoked"},
		},
	}

	restoreMaskedSecrets(incoming, stored)

	if incoming.ClientSecret != "primary-secret" {
		t.Fatalf("primary secret = %q, want the stored value restored", incoming.ClientSecret)
	}
	if incoming.Keys[0].Secret != "secret-one" {
		t.Fatalf("key k1 secret = %q, want it restored (not the mask)", incoming.Keys[0].Secret)
	}
	if incoming.Keys[1].Secret != "secret-two" {
		t.Fatalf("key k2 secret = %q, want it restored (not the mask)", incoming.Keys[1].Secret)
	}
	// The edited non-secret field survives — restore touches only the secret.
	if incoming.Keys[1].State != "revoked" {
		t.Fatalf("key k2 state = %q, want the edit preserved", incoming.Keys[1].State)
	}
}

// A genuinely new secret is NOT restored — it must pass through so a real
// rotation replaces the stored one.
func TestRestoreMaskedSecrets_LetsNewSecretThrough(t *testing.T) {
	stored := &Provider{
		Owner: "acme", Name: "aws",
		Keys: []ProviderKey{{Name: "k1", Secret: "old"}},
	}
	incoming := &Provider{
		Owner: "acme", Name: "aws",
		Keys: []ProviderKey{{Name: "k1", Secret: "brand-new"}},
	}

	restoreMaskedSecrets(incoming, stored)

	if incoming.Keys[0].Secret != "brand-new" {
		t.Fatalf("key k1 secret = %q, want the new value to pass through", incoming.Keys[0].Secret)
	}
}

// fakeVault is an in-memory Vault — the whole point being that sealing and
// resolving are exercised with no live KMS.
type fakeVault struct{ m map[string][]byte }

func newFakeVault() *fakeVault { return &fakeVault{m: map[string][]byte{}} }

func (f *fakeVault) Set(key string, value []byte) error {
	f.m[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeVault) Get(key string) ([]byte, error) {
	v, ok := f.m[key]
	if !ok {
		return nil, fmt.Errorf("fake kms: %s not found", key)
	}
	return v, nil
}

// With a vault wired, a secret is sealed to a reference the row can hold, and the
// reference resolves back to the plaintext. The reference is not the plaintext.
func TestSealOpenSecret_RoundTripsThroughVault(t *testing.T) {
	kms.SetVault(newFakeVault())
	t.Cleanup(func() { kms.SetVault(nil) })

	key := providerSecretKey("acme", "aws", "clientSecret")
	stored := sealSecret(key, "super-secret-token")

	if stored == "super-secret-token" {
		t.Fatal("sealed value must not be the plaintext")
	}
	if !kms.IsRef(stored) {
		t.Fatalf("sealed value %q must be a reference", stored)
	}
	if got := openSecret(stored); got != "super-secret-token" {
		t.Fatalf("openSecret = %q, want the plaintext resolved", got)
	}
}

// Without a vault, sealing is a no-op (plaintext stays) and resolving a plaintext
// returns it unchanged — the pre-KMS behaviour, so an unconfigured deployment is
// untouched.
func TestSealOpenSecret_PlaintextWhenUnconfigured(t *testing.T) {
	kms.SetVault(nil)

	key := providerSecretKey("acme", "aws", "clientSecret")
	if got := sealSecret(key, "plain"); got != "plain" {
		t.Fatalf("sealSecret unconfigured = %q, want the plaintext kept", got)
	}
	if got := openSecret("plain"); got != "plain" {
		t.Fatalf("openSecret of a plaintext = %q, want it unchanged", got)
	}
}
