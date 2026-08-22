// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

package routers

// goneAssets maps each retired ASSET address to the address that replaces it.
// The mechanism is in gone.go; this file is only the table.
//
// Five verbs collapse onto one collection and one member, and which of the five
// you meant is the method you send. The value is a list because RFC 5829,
// section 3.6 admits more than one successor-version link — a verb that meant
// two things splits when it becomes a resource.
//
// A successor with a variable segment is written as an RFC 6570 template, which
// is the spelling the OpenAPI document uses for the same address. One address,
// one string, wherever a client reads it — the router's own ":owner" is an
// internal spelling and belongs nowhere a caller can see.
//
// This table only ever grows by retirement and shrinks by deletion. Nothing
// else in visor names these addresses, and every successor here is an address
// visor really serves (TestRetiredAssetSuccessorIsServed).
var goneAssets = map[string][]string{
	// The asset itself. get-asset took ?id=owner/name; the pair is the address.
	"/v1/get-assets":   {"/v1/assets"},
	"/v1/get-asset":    {"/v1/assets/{owner}/{name}"},
	"/v1/add-asset":    {"/v1/assets"},
	"/v1/update-asset": {"/v1/assets/{owner}/{name}"},
	"/v1/delete-asset": {"/v1/assets/{owner}/{name}"},

	// The two remote-access doors, whose old names described neither what they
	// made nor what they addressed. add-asset-tunnel made no tunnel: it opened a
	// SESSION on an asset, which is a sub-resource of the asset. get-asset-tunnel
	// addressed no asset: it read ?sessionId= and carried the stream of THAT
	// session, which is a sub-resource of the session.
	"/v1/add-asset-tunnel": {"/v1/assets/{owner}/{name}/sessions"},
	"/v1/get-asset-tunnel": {"/v1/sessions/{owner}/{name}/tunnel"},
}
