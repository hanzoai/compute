// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

package service

import (
	"net/http"
	"testing"
)

// Hanzo's OWN DigitalOcean account is reached the same way every per-row account
// is: through the carrier. Carried, this process holds no token — that is the
// point of egress — so an empty token is the correct state and not a
// misconfiguration.
//
// Before, the platform path asked for the token first and refused without one,
// and it handed the SDK a nil http.Client, so it could never have been carried
// even with egress configured. Both halves are checked here: that it stops
// demanding a token, and that the request would actually go through the carrier.
func TestPlatformDigitalOceanIsCarried(t *testing.T) {
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	t.Run("no carrier and no token still fails closed", func(t *testing.T) {
		RegisterCarrier(nil)
		if _, err := newDigitalOceanClient(); err == nil {
			t.Fatal("an unconfigured account with no carrier must refuse: nothing can reach DigitalOcean")
		}
		if ComputeConfigured() {
			t.Error("ComputeConfigured() = true with neither token nor carrier")
		}
	})

	t.Run("carried needs no token", func(t *testing.T) {
		t.Cleanup(func() { RegisterCarrier(nil) })
		var saw Credential
		carried := &http.Client{}
		RegisterCarrier(func(c Credential) (*http.Client, error) { saw = c; return carried, nil })

		if !ComputeConfigured() {
			t.Error("ComputeConfigured() = false under a carrier — every platform endpoint would answer 503 " +
				"on an account egress can reach")
		}
		if _, err := newDigitalOceanClient(); err != nil {
			t.Fatalf("carried client: %v — the token is egress's, so its absence here is not a fault", err)
		}
		if saw.Provider != providerDigitalOcean {
			t.Errorf("carrier asked for provider %q, want %q — it was not consulted for the platform account",
				saw.Provider, providerDigitalOcean)
		}
	})

	t.Run("the platform account is still listed when carried", func(t *testing.T) {
		t.Cleanup(func() { RegisterCarrier(nil) })
		RegisterCarrier(func(Credential) (*http.Client, error) { return &http.Client{}, nil })

		got := cloudProviders()
		if len(got) != 1 || got[0].provider != providerDigitalOcean {
			t.Fatalf("platform providers = %+v, want the DigitalOcean account — hiding it would drop "+
				"Hanzo's own clusters from every reader the moment the token left this pod", got)
		}
		if got[0].secret != "" {
			t.Error("a carried provider must carry no secret: egress attaches it")
		}
	})
}
