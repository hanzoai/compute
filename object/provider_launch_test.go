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
	"fmt"
	"testing"
)

// TestLaunchCredentials pins the enumeration: the provider's own credential
// leads, active keys follow in declared order, a key's region inherits the row
// when blank, and a disabled or empty key is omitted.
func TestLaunchCredentials(t *testing.T) {
	p := &Provider{
		Type: "DigitalOcean", Name: "do", State: "Active",
		ClientSecret: "row-token", Region: "sfo3",
		Keys: []ProviderKey{
			{Name: "a", Secret: "tok-a", Region: "nyc1"},          // own region
			{Name: "b", Secret: "tok-b"},                          // inherits sfo3
			{Name: "rl", Secret: "tok-rl", State: "rate-limited"}, // out of rotation
			{Name: "empty"},                                       // no credential
			{Name: "c", Secret: "tok-c", State: "active"},         // explicit active
		},
	}
	got := p.LaunchCredentials()
	want := []LaunchCredential{
		{KeyName: "", KeyID: "", Secret: "row-token", Region: "sfo3"},
		{KeyName: "a", Secret: "tok-a", Region: "nyc1"},
		{KeyName: "b", Secret: "tok-b", Region: "sfo3"},
		{KeyName: "c", Secret: "tok-c", Region: "sfo3"},
	}
	if len(got) != len(want) {
		t.Fatalf("LaunchCredentials len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cred[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLaunchCredentialsAllKeys pins the canonical multi-account shape: a
// provider that leaves its row credential empty and lists every account as a Key
// launches only on the Keys, each independently skippable by its own State. The
// row's own credential appears only when the row actually carries one.
func TestLaunchCredentialsAllKeys(t *testing.T) {
	p := &Provider{
		Type: "DigitalOcean", Name: "do", State: "Active", Region: "sfo3",
		Keys: []ProviderKey{
			{Name: "a", Secret: "tok-a"},
			{Name: "b", Secret: "tok-b", State: "rate-limited"},
			{Name: "c", Secret: "tok-c"},
		},
	}
	got := p.LaunchCredentials()
	if len(got) != 2 {
		t.Fatalf("empty row cred + 3 keys (1 skipped) = %d, want 2: %+v", len(got), got)
	}
	if got[0].KeyName != "a" || got[1].KeyName != "c" {
		t.Errorf("usable keys = %q,%q, want a,c", got[0].KeyName, got[1].KeyName)
	}
}

// TestPickLaunchCredentialRoundRobin pins that a rate-limited account is skipped
// and the cursor rotates across exactly the usable set, covering every one.
func TestPickLaunchCredentialRoundRobin(t *testing.T) {
	p := &Provider{
		Type: "DigitalOcean", Name: "do", State: "Active",
		ClientSecret: "row-token", Region: "sfo3",
		Keys: []ProviderKey{
			{Name: "a", Secret: "tok-a"},
			{Name: "rl", Secret: "tok-rl", State: "rate-limited"},
			{Name: "b", Secret: "tok-b"},
		},
	}
	creds := p.LaunchCredentials() // "", a, b — rl skipped
	if len(creds) != 3 {
		t.Fatalf("usable set = %d, want 3 (rl skipped): %+v", len(creds), creds)
	}

	seen := map[string]int{}
	for cursor := uint64(0); cursor < 6; cursor++ { // two full turns
		c, ok := pickLaunchCredential(creds, cursor)
		if !ok {
			t.Fatalf("cursor %d: ok=false over a non-empty set", cursor)
		}
		if c.Secret == "tok-rl" {
			t.Fatalf("cursor %d landed on the rate-limited account", cursor)
		}
		seen[c.KeyName]++
	}
	for _, name := range []string{"", "a", "b"} {
		if seen[name] != 2 {
			t.Errorf("account %q used %d times over two turns, want 2", name, seen[name])
		}
	}

	if _, ok := pickLaunchCredential(nil, 0); ok {
		t.Errorf("empty set must return ok=false")
	}
}

// TestLaunchCredentialForRotates pins the per-provider cursor: consecutive
// launches on one provider land on consecutive accounts, so a burst spreads
// instead of pinning one account.
func TestLaunchCredentialForRotates(t *testing.T) {
	p := &Provider{
		Type: "DigitalOcean", Name: fmt.Sprintf("rot-%d", testProviderSeq()), State: "Active",
		ClientSecret: "row-token", Region: "sfo3",
		Keys: []ProviderKey{{Name: "a", Secret: "tok-a"}},
	}
	first, ok1 := p.LaunchCredentialFor()
	second, ok2 := p.LaunchCredentialFor()
	if !ok1 || !ok2 {
		t.Fatalf("LaunchCredentialFor ok = %v,%v want true,true", ok1, ok2)
	}
	if first.KeyName == second.KeyName {
		t.Errorf("two consecutive launches pinned account %q; expected rotation", first.KeyName)
	}
}

// TestLaunchCredentialNamed pins that a machine's recorded account resolves back
// to its credential, and a vanished account reports ok=false rather than a
// zero credential the caller would launch on.
func TestLaunchCredentialNamed(t *testing.T) {
	p := &Provider{
		Type: "DigitalOcean", Name: "do", State: "Active",
		ClientSecret: "row-token", Region: "sfo3",
		Keys: []ProviderKey{{Name: "a", Secret: "tok-a", Region: "nyc1"}},
	}
	if c, ok := p.launchCredentialNamed(""); !ok || c.Secret != "row-token" {
		t.Errorf(`named("") = %+v ok=%v, want row-token`, c, ok)
	}
	if c, ok := p.launchCredentialNamed("a"); !ok || c.Secret != "tok-a" || c.Region != "nyc1" {
		t.Errorf(`named("a") = %+v ok=%v, want tok-a/nyc1`, c, ok)
	}
	if _, ok := p.launchCredentialNamed("gone"); ok {
		t.Errorf(`named("gone") ok=true, want false`)
	}
}

// TestGetMaskedProviderMasksKeys pins the leak fix: a rotation key is a
// credential and masks like the row's own secret; name and region stay visible.
func TestGetMaskedProviderMasksKeys(t *testing.T) {
	p := &Provider{
		ClientSecret: "row-secret",
		Keys: []ProviderKey{
			{Name: "a", Secret: "tok-a", Region: "nyc1"},
			{Name: "b", Secret: "tok-b"},
		},
	}
	masked, err := GetMaskedProvider(p)
	if err != nil {
		t.Fatal(err)
	}
	if masked.ClientSecret != "***" {
		t.Errorf("ClientSecret = %q, want ***", masked.ClientSecret)
	}
	for i, k := range masked.Keys {
		if k.Secret != "***" {
			t.Errorf("Keys[%d].Secret = %q, want *** (credential leak)", i, k.Secret)
		}
	}
	if masked.Keys[0].Name != "a" || masked.Keys[0].Region != "nyc1" {
		t.Errorf("masking must keep name/region visible, got %+v", masked.Keys[0])
	}
}

// testProviderSeq returns a fresh int each call so rotation tests use a distinct
// provider id and never share a cursor.
var providerSeq int

func testProviderSeq() int { providerSeq++; return providerSeq }

// TestCredentialLabelSelectsAccount pins the egress label mapping: the row's own
// account carries the provider's own label (unchanged single-account behavior),
// and each additional key carries its own name, so a carried launch resolves a
// DISTINCT KMS credential per account instead of collapsing them to one.
func TestCredentialLabelSelectsAccount(t *testing.T) {
	p := &Provider{Type: "DigitalOcean", Name: "do-prod", Region: "sfo3"}
	creds := []LaunchCredential{
		{KeyName: "", Secret: "row-token", Region: "sfo3"},
		{KeyName: "do-2", Secret: "tok-2", Region: "nyc1"},
	}
	labels := map[string]string{}
	for _, c := range creds {
		cred := p.credential(c)
		labels[c.KeyName] = cred.Name
	}
	if labels[""] != "do-prod" {
		t.Errorf(`row account label = %q, want the provider's own name "do-prod"`, labels[""])
	}
	if labels["do-2"] != "do-2" {
		t.Errorf(`key account label = %q, want its own name "do-2"`, labels["do-2"])
	}
	if labels[""] == labels["do-2"] {
		t.Errorf("two accounts share label %q — a carried launch would collapse them to one KMS key", labels[""])
	}
}
