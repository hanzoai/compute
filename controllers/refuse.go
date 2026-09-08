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

// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package controllers

// What visor says when it will not answer.
//
// Each of these was written out at several call sites, so each was several
// slightly different sentences for one fact. They are values now, said the same
// way everywhere.
//
// A refusal names the MISSING FACT and where it comes from. "not configured"
// answers none of the three questions a caller actually has — is my request
// wrong, is my credential wrong, or is this deployment missing something? — and
// a caller who cannot tell those apart retries the one thing that will never
// work.
const (
	// The request named no organization. Both ways of naming one are given,
	// because which is right depends on who is asking: a person's token carries
	// its own, a service's token does not and must say.
	refuseNoOrg = "no organization in this request: send a bearer token whose subject carries one, or name it with ?owner="

	// The DEPLOYMENT has no compute. Nothing the caller sends fixes this, which
	// is why it does not read like a request error.
	refuseNoCompute = "compute is not configured on this deployment: no cloud account is installed, so there is nothing to launch on"

	// The ORGANIZATION has no cloud. This one the caller CAN fix, and the
	// address that fixes it is named.
	refuseNoProvider = "this organization has no cloud provider: add one at /v1/providers, then launch"

	// A required parameter, with the values it takes.
	refuseNoPool         = "the pool is named by the address: /v1/pools/{owner}/{name}"
	refuseNoProviderName = "name the provider: ?provider=<name>, one of the providers this organization has added"

	// No credential at all.
	refuseNotSignedIn = "not signed in: send a bearer token"
)
