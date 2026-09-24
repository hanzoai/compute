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
	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/service"
)

// RegisterCloudCredentials teaches service which cloud accounts this deployment
// may spend on: the active cloud Providers of the reserved SuperAdmin org, the
// one org whose rows are the platform's own accounts.
//
// It is the reader half of service.RegisterCredentials. The rows live here and
// service cannot import object (object imports service), so the source is handed
// inward — the same direction, and for the same reason, as RegisterMembership.
func RegisterCloudCredentials() {
	owner := service.SuperAdminOrg
	service.RegisterCredentials(func() ([]service.Credential, error) {
		providers, err := getActiveCloudProviders(owner)
		if err != nil {
			// Loud, and returned. Accounts that cannot be read are not "no
			// accounts": the pool sweep bills from the live pools of these
			// accounts, and reading an unreadable store as empty would bill
			// stale rows instead.
			logs.Warning("cloud credentials: cannot read providers for %s: %v", owner, err)
			return nil, err
		}
		out := make([]service.Credential, 0, len(providers))
		for _, p := range providers {
			out = append(out, service.Credential{
				Provider: p.Type,
				Name:     p.Name,
				KeyID:    p.ClientId,
				Secret:   p.ClientSecret,
				Region:   p.Region,
			})
		}
		return out, nil
	})
}
