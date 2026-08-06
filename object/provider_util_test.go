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
	"strings"
	"testing"
)

// TestIsActiveCloudProvider pins the widened predicate: the live prod DO row
// carries its token in ClientId (ClientSecret empty) with Category "Cloud", and
// must be accepted alongside the legacy Public/Private Cloud shapes.
func TestIsActiveCloudProvider(t *testing.T) {
	cases := []struct {
		name string
		p    *Provider
		want bool
	}{
		{"prod DO row: token in ClientId, Category Cloud", &Provider{ClientId: "dop_v1_token", Category: "Cloud", State: "Active"}, true},
		{"token in ClientSecret, Public Cloud", &Provider{ClientSecret: "secret", Category: "Public Cloud", State: "Active"}, true},
		{"token in ClientId, Private Cloud", &Provider{ClientId: "token", Category: "Private Cloud", State: "Active"}, true},
		{"both tokens set, Cloud", &Provider{ClientId: "a", ClientSecret: "b", Category: "Cloud", State: "Active"}, true},
		{"no token rejected", &Provider{Category: "Cloud", State: "Active"}, false},
		{"inactive state rejected", &Provider{ClientId: "token", Category: "Cloud", State: "Inactive"}, false},
		{"empty state rejected", &Provider{ClientId: "token", Category: "Cloud", State: ""}, false},
		{"blockchain category rejected", &Provider{ClientId: "token", Category: "Blockchain", State: "Active"}, false},
		{"unknown category rejected", &Provider{ClientId: "token", Category: "Storage", State: "Active"}, false},
	}
	for _, tc := range cases {
		if got := isActiveCloudProvider(tc.p); got != tc.want {
			t.Errorf("%s: isActiveCloudProvider = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestMaskedProviderCarriesNoToken is the same fact stated on the read side, and
// the two together are the reason it matters: the predicate above accepts a row
// whose token is in ClientId because that is the LIVE production shape, and the
// mask used to publish exactly that slot.
//
// A DigitalOcean account is one API token. Every driver reads ClientSecret and
// falls back to ClientId, so both slots are the credential and a read that
// masks one of them hands out the other.
func TestMaskedProviderCarriesNoToken(t *testing.T) {
	for name, p := range map[string]*Provider{
		"prod DO row: token in ClientId": {Owner: "acme", Name: "do", Category: "Cloud", ClientId: "dop_v1_live_token"},
		"token in ClientSecret":          {Owner: "acme", Name: "do", Category: "Cloud", ClientSecret: "dop_v1_live_token"},
		"both slots set":                 {Owner: "acme", Name: "do", Category: "Cloud", ClientId: "id_token", ClientSecret: "secret_token"},
		"an empty slot stays empty":      {Owner: "acme", Name: "do", Category: "Cloud", ClientId: "only_id"},
		"a blockchain row is masked too": {Owner: "acme", Name: "chain", Category: "Blockchain", ClientId: "key", ClientSecret: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := GetMaskedProvider(p)
			if err != nil {
				t.Fatalf("GetMaskedProvider: %v", err)
			}
			if strings.Contains(got.ClientId, "token") || strings.Contains(got.ClientId, "key") || strings.Contains(got.ClientId, "only_id") {
				t.Fatalf("ClientId = %q — a live credential slot was published in cleartext", got.ClientId)
			}
			if strings.Contains(got.ClientSecret, "token") || strings.Contains(got.ClientSecret, "secret") {
				t.Fatalf("ClientSecret = %q — a live credential slot was published in cleartext", got.ClientSecret)
			}
		})
	}
}

// TestMaskedProviderEmptySlotStaysEmpty is the control: masking is not "write
// *** everywhere". A slot the row does not use must come back empty, because an
// edit sends the whole object back and "***" means "unchanged" — a mask over an
// empty slot would restore nothing and read as a credential that is there.
func TestMaskedProviderEmptySlotStaysEmpty(t *testing.T) {
	got, err := GetMaskedProvider(&Provider{Owner: "acme", Name: "do", ClientId: "dop_v1_live_token"})
	if err != nil {
		t.Fatalf("GetMaskedProvider: %v", err)
	}
	if got.ClientId != masked {
		t.Fatalf("ClientId = %q, want the mask", got.ClientId)
	}
	if got.ClientSecret != "" {
		t.Fatalf("ClientSecret = %q, want empty — the row has no secret to mask", got.ClientSecret)
	}
}

// TestUpdateProviderKeepsTheTokenThroughTheMask is the other half of masking a
// slot, and without it the mask is a data-loss bug: an editor reads the provider
// (masked), changes the region, and saves the whole object back — so the token
// arrives as "***". Restoring it is what makes the read safe to mask.
//
// The live production row keeps its token in ClientId, so that is the slot this
// drives.
func TestUpdateProviderKeepsTheTokenThroughTheMask(t *testing.T) {
	installBaseStore(t)

	const token = "dop_v1_live_token"
	if _, err := AddProvider(&Provider{
		Owner: "acme", Name: "housedo", Category: "Cloud", Type: "DigitalOcean",
		State: "Active", ClientId: token, Region: "nyc3",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	// Exactly what an editor sends: the masked read, with one field changed.
	edit, err := GetMaskedProvider(getProvider("acme", "housedo"))
	if err != nil {
		t.Fatalf("GetMaskedProvider: %v", err)
	}
	edit.Region = "sfo3"
	if _, err := UpdateProvider("acme/housedo", edit); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	stored, err := getProvider("acme", "housedo")
	if err != nil {
		t.Fatalf("getProvider: %v", err)
	}
	if stored.ClientId != token {
		t.Fatalf("ClientId = %q, want the stored token — the mask was written over the credential", stored.ClientId)
	}
	if stored.Region != "sfo3" {
		t.Fatalf("Region = %q, want the edit to land", stored.Region)
	}
}
