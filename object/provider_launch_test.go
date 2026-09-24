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
	"errors"
	"fmt"
	"testing"
)

// TestLaunchCredentials pins the enumeration: the provider's own account leads,
// active keys follow in declared order, a key's region inherits the row when
// blank, and a disabled or unnamed key is omitted. Every entry is a label in
// egress custody; none carries a key.
func TestLaunchCredentials(t *testing.T) {
	p := &Provider{
		Type: "Hetzner", Name: "hz", State: "Active", Region: "fsn1",
		Keys: []ProviderKey{
			{Name: "a", Region: "nbg1"},         // own region
			{Name: "b"},                         // inherits fsn1
			{Name: "rl", State: "rate-limited"}, // out of rotation
			{Name: ""},                          // no label
			{Name: "c", State: "active"},        // explicit active
		},
	}
	got := p.LaunchCredentials()
	want := []LaunchCredential{
		{KeyName: "", Region: "fsn1"},
		{KeyName: "a", Region: "nbg1"},
		{KeyName: "b", Region: "fsn1"},
		{KeyName: "c", Region: "fsn1"},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestPickLaunchCredentialRoundRobin pins that a rate-limited account is skipped
// and the cursor rotates across exactly the usable set, covering every one.
func TestPickLaunchCredentialRoundRobin(t *testing.T) {
	p := &Provider{
		Type: "Hetzner", Name: "hz", State: "Active", Region: "fsn1",
		Keys: []ProviderKey{{Name: "a"}, {Name: "rl", State: "rate-limited"}, {Name: "b"}},
	}
	creds := p.LaunchCredentials() // "", a, b — rl skipped
	if len(creds) != 3 {
		t.Fatalf("usable set = %d, want 3 (rl skipped): %+v", len(creds), creds)
	}
	seen := map[string]int{}
	for cursor := range uint64(6) { // two full turns
		c, ok := pickLaunchCredential(creds, cursor)
		if !ok {
			t.Fatalf("cursor %d: ok=false over a non-empty set", cursor)
		}
		if c.KeyName == "rl" {
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
		Type: "Hetzner", Name: fmt.Sprintf("rot-%d", testProviderSeq()), State: "Active", Region: "fsn1",
		Keys: []ProviderKey{{Name: "a"}},
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
// to its account, and a vanished account reports ok=false rather than a zero
// account the caller would launch on.
func TestLaunchCredentialNamed(t *testing.T) {
	p := &Provider{
		Type: "Hetzner", Name: "hz", State: "Active", Region: "fsn1",
		Keys: []ProviderKey{{Name: "a", Region: "nbg1"}},
	}
	if c, ok := p.launchCredentialNamed(""); !ok || c.Region != "fsn1" {
		t.Errorf(`named("") = %+v ok=%v, want the row's own account`, c, ok)
	}
	if c, ok := p.launchCredentialNamed("a"); !ok || c.Region != "nbg1" {
		t.Errorf(`named("a") = %+v ok=%v, want a/nbg1`, c, ok)
	}
	if _, ok := p.launchCredentialNamed("gone"); ok {
		t.Errorf(`named("gone") ok=true, want false`)
	}
}

// A provider write carrying a cloud key is refused, whichever field carries it;
// one that names only accounts is not.
func TestAProviderWriteCarryingAKeyIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"owner":"acme","name":"hz","clientSecret":"tok"}`,
		`{"owner":"acme","name":"hz","clientId":"AKIAxxxx"}`,
		`{"owner":"acme","name":"hz","keys":[{"name":"a","secret":"tok-a"}]}`,
		`{"owner":"acme","name":"hz","keys":[{"name":"a","keyId":"AKIA"}]}`,
	} {
		if err := RefuseProviderKeys([]byte(body)); !errors.Is(err, ErrKeyNotStored) {
			t.Errorf("%s = %v, want ErrKeyNotStored", body, err)
		}
	}
	if err := RefuseProviderKeys([]byte(`{"owner":"acme","name":"hz","type":"Hetzner","keys":[{"name":"a","region":"nbg1"}]}`)); err != nil {
		t.Errorf("a key-less provider was refused: %v", err)
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
		{KeyName: "", Region: "sfo3"},
		{KeyName: "do-2", Region: "nyc1"},
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

// A provider row is its owner's account unless the reserved SuperAdmin org owns
// it: a tenant's row says whose it is, so the carrier can refuse to spend it as
// compute, and only admin's rows are the platform's accounts — not hanzo's, whose
// members are every Hanzo user, and not any org named in configuration.
func TestACredentialSaysWhoseAccountItIs(t *testing.T) {
	t.Setenv("platformOwner", "hanzo")
	for _, owner := range []string{"mallory", "hanzo", "Admin", "admin-org"} {
		c := (&Provider{Owner: owner, Type: "AWS", Name: "hanzo-compute"}).credential(LaunchCredential{})
		if c.Tenant != owner {
			t.Errorf("%s's row reads as tenant %q", owner, c.Tenant)
		}
	}
	platform := (&Provider{Owner: "admin", Type: "DigitalOcean", Name: "do-prod"}).credential(LaunchCredential{})
	if platform.Tenant != "" {
		t.Errorf("the SuperAdmin org's row reads as tenant %q", platform.Tenant)
	}
}
