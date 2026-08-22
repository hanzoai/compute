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

package routers

// The addresses visor used to serve, and the resource that replaced each.
//
// The successor is a COLLECTION or an ITEM a caller can dereference, never a
// router pattern: RFC 8288 wants a URI reference in a Link, and "/v1/assets/
// :owner/:name" is neither a URL nor an RFC 6570 template. Where the old
// address named one thing, the successor is the collection it belongs to — the
// caller knows its own key and can address the item from there.
func init() {
	// Assets.
	Retire("/v1/get-assets", "/v1/assets")
	Retire("/v1/get-asset", "/v1/assets")
	Retire("/v1/add-asset", "/v1/assets")
	Retire("/v1/update-asset", "/v1/assets")
	Retire("/v1/delete-asset", "/v1/assets")

	// Providers.
	Retire("/v1/get-providers", "/v1/providers")
	Retire("/v1/get-provider", "/v1/providers")
	Retire("/v1/add-provider", "/v1/providers")
	Retire("/v1/update-provider", "/v1/providers")
	Retire("/v1/delete-provider", "/v1/providers")

	// Records.
	Retire("/v1/get-records", "/v1/records")
	Retire("/v1/get-record", "/v1/records")
	Retire("/v1/add-record", "/v1/records")
	Retire("/v1/update-record", "/v1/records")
	Retire("/v1/delete-record", "/v1/records")

	// Sessions.
	Retire("/v1/get-sessions", "/v1/sessions")
	Retire("/v1/get-session", "/v1/sessions")
	Retire("/v1/add-session", "/v1/sessions")
	Retire("/v1/update-session", "/v1/sessions")
	Retire("/v1/delete-session", "/v1/sessions")

	// Plans.
	Retire("/v1/get-plans", "/v1/plans")
	Retire("/v1/get-plan", "/v1/plans")
	Retire("/v1/add-plan", "/v1/plans")
	Retire("/v1/update-plan", "/v1/plans")
	Retire("/v1/delete-plan", "/v1/plans")

	// The two that were called asset tunnels. Each successor is the collection
	// the thing actually belongs to: creating a session is a POST to the asset's
	// sessions, and the live connection belongs to the session it connects to.
	Retire("/v1/add-asset-tunnel", "/v1/assets")
	Retire("/v1/get-asset-tunnel", "/v1/sessions")

	// Node pools.
	Retire("/v1/get-node-pools", "/v1/pools")
	Retire("/v1/get-node-pool", "/v1/pools")
	Retire("/v1/create-node-pool", "/v1/pools")
	Retire("/v1/update-node-pool", "/v1/pools")
	Retire("/v1/delete-node-pool", "/v1/pools")
	Retire("/v1/scale-node-pool", "/v1/pools")

	// Volumes.
	Retire("/v1/get-volumes", "/v1/volumes")
	Retire("/v1/get-volume", "/v1/volumes")
	Retire("/v1/create-volume", "/v1/volumes")
	Retire("/v1/delete-volume", "/v1/volumes")
	Retire("/v1/attach-volume", "/v1/volumes")
	Retire("/v1/detach-volume", "/v1/volumes")
	Retire("/v1/resize-volume", "/v1/volumes")

	// The four that were state changes wearing verbs. Each is a property of the
	// thing it changes: a session's status, a record's block.
	Retire("/v1/start-session", "/v1/sessions")
	Retire("/v1/stop-session", "/v1/sessions")
	Retire("/v1/commit-record", "/v1/records")
	Retire("/v1/query-record", "/v1/records")

	// The caller's own account, and the deployment's branding. Each was the only
	// thing at its address, so the address is simply the thing.
	Retire("/v1/get-account", "/v1/account")
	Retire("/v1/get-whitelabel", "/v1/whitelabel")

	// Machines. There were two collections and a caller had to join them; there
	// is one now, and the join is the server's.
	Retire("/v1/get-machines", "/v1/machines")
	Retire("/v1/get-machine", "/v1/machines")
	Retire("/v1/add-machine", "/v1/machines")
	Retire("/v1/update-machine", "/v1/machines")
	Retire("/v1/delete-machine", "/v1/machines")
	Retire("/v1/launch-machine", "/v1/machines")
	Retire("/v1/machines/launch", "/v1/machines")
}

// NOT RETIRED, AND THE REASON IS MEASURED.
//
// Volumes and node pools keep their verb addresses. Their identity is
// (organization from the PRINCIPAL, provider, name) — controllers/volume.go
// reads ?name= and ?provider=, and node_pool.go composes poolId(resolveComputeOrg(),
// name). No owner is addressed, so an item address has one segment, and
// pathTarget reads the first segment after the kind as the OWNER:
//
//	/v1/volumes/vol-1   -> owner="vol-1"  name=""
//	/v1/pools/gpu       -> owner="gpu"    name=""
//	/v1/plans/acme/pro  -> owner="acme"   name="pro"
//
// So the seam would compare a subject against a VOLUME NAME. subOwner ==
// "vol-1" is false for every subject, which denies everyone — the same shape as
// the empty-owner failure the path grammar exists to prevent, reached from the
// other direction.
//
// Giving them the grammar means deciding whether the provider belongs in the
// address and whether the org should be named rather than inferred. That is a
// change to what these endpoints ARE, so it lands with their callers, not in a
// rename.
