package service

import (
	iam "github.com/hanzoai/iam"
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
// configured at startup (controllers.InitAuthConfig -> iam.InitConfig), so the
// global client is ready by the time any launch runs.
func registerBotUser(org, name, displayName string) {
	if org == "" || name == "" {
		return
	}
	if displayName == "" {
		displayName = name
	}
	_, _ = iam.AddUser(&iam.User{
		Owner:       org,
		Name:        name,
		DisplayName: displayName,
		Type:        "service-account",
		Tag:         "agent",
	})
}
