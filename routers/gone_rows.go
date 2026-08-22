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
}
