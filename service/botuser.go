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
	"github.com/hanzoai/iamsdk/v2/iamsdk"
)

// botuser.go — a launched bot IS an org member. registerBotUser creates an IAM
// user for each machine at launch so the bot shows up as a user everywhere IAM
// identities surface — notably the hanzo.team roster — instead of on a separate
// bot dashboard. The bot is a passwordless "service-account" tagged "agent": it
// never logs in interactively; it authenticates to the gateway by token.
//
// Best-effort by design: a registration failure never fails the launch (the
// machine + bot still run; the identity reconciles later). Idempotent — a
// re-launch of a same-named fleet member no-ops on the existing user. IAM is
// configured at startup (controllers.InitAuthConfig -> iamsdk.InitConfig), so the
// global client is ready by the time any launch runs.
func registerBotUser(org, name, displayName string) {
	if org == "" || name == "" {
		return
	}
	if displayName == "" {
		displayName = name
	}
	_, _ = iamsdk.AddUser(&iamsdk.User{
		Owner:       org,
		Name:        name,
		DisplayName: displayName,
		Type:        "service-account",
		Tag:         "agent",
	})
}
