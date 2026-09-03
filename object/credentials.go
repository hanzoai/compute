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

package object

import (
	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/service"
)

// RegisterCloudCredentials teaches service which cloud accounts this deployment
// may spend on: the active cloud Providers owned by owner.
//
// It is the reader half of service.RegisterCredentials. The rows live here and
// service cannot import object (object imports service), so the source is handed
// inward — the same direction, and for the same reason, as RegisterMembership.
//
// An empty owner registers nothing, and service falls back to the single
// configured DigitalOcean token. That is the deployment that has not been told
// which org holds its accounts, and it behaves exactly as it did before.
func RegisterCloudCredentials(owner string) {
	if owner == "" {
		return
	}
	service.RegisterCredentials(func() []service.Credential {
		providers, err := getActiveCloudProviders(owner)
		if err != nil {
			// Loud, and not fatal. service falls back to the single token, so a
			// store that cannot be read costs the extra accounts rather than
			// every account — but an operator has to know the difference, or a
			// fleet that quietly shrank to one cloud reads as one cloud.
			logs.Warning("cloud credentials: cannot read providers for %s, falling back to the configured token: %v", owner, err)
			return nil
		}
		out := make([]service.Credential, 0, len(providers))
		for _, p := range providers {
			out = append(out, service.Credential{
				Provider: p.Type,
				Name:     p.Name,
				KeyID:    p.ClientId,
				// Resolve from KMS instead of reading the plaintext column; a legacy
				// plaintext row resolves to itself (dual-read).
				Secret: openSecret(p.ClientSecret),
				Region: p.Region,
			})
		}
		return out
	})
}
