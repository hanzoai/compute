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

import (
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/controllers"
)

// registerAsset declares an ASSET — a reachable machine visor can open a remote
// session on (controllers/asset.go). Five TYPED ops, so this noun is in the
// registry every projection reads (OpenAPI, MCP, CLI, the generated SDKs, the
// by-name call plane) rather than only on the wire.
//
// ONE collection, ONE member, and the METHOD carries the verb. The member is
// addressed by the (owner, name) pair, which IS an asset's identity — the same
// address hanzoai/iam gives a user. It replaces five verb-in-path addresses
// where three spelled the target three different ways: ?id=owner/name on the
// read and the update, a whole asset in the body on the delete.
func registerAsset(app *zip.App) {
	zip.Get(app, "/v1/assets", controllers.ListAssets,
		zip.WithSummary("List an org's assets"),
		zip.WithOperationID("listAssets"),
		zip.WithTags("Asset"),
	)
	zip.Post(app, "/v1/assets", controllers.CreateAsset,
		zip.WithSummary("Create an asset"),
		zip.WithOperationID("createAsset"),
		zip.WithTags("Asset"),
		zip.WithStatus(201),
	)
	zip.Get(app, "/v1/assets/:owner/:name", controllers.GetAsset,
		zip.WithSummary("Read one asset"),
		zip.WithOperationID("getAsset"),
		zip.WithTags("Asset"),
	)
	zip.Put(app, "/v1/assets/:owner/:name", controllers.UpdateAsset,
		zip.WithSummary("Replace one asset"),
		zip.WithOperationID("updateAsset"),
		zip.WithTags("Asset"),
	)
	zip.Delete(app, "/v1/assets/:owner/:name", controllers.DeleteAsset,
		zip.WithSummary("Remove one asset"),
		zip.WithOperationID("deleteAsset"),
		zip.WithTags("Asset"),
	)
}

// registerTunnel declares the two doors of a REMOTE SESSION: opening one on an
// asset, and the websocket that carries it (controllers/tunnel.go).
//
// Each hangs off the noun it actually belongs to, which the old names got the
// wrong way round. POST /v1/add-asset-tunnel made no tunnel — it made a
// session, and an asset is that session's parent. GET /v1/get-asset-tunnel
// addressed no asset — it read ?sessionId= and has always carried the stream of
// THAT session, so the session is its parent.
//
// The tunnel's address is a sub-resource under /v1/sessions, which is the
// SESSION noun's collection. Nothing about that address collides with the
// member address that noun eventually takes: fiber matches on segment count
// first, so /v1/sessions/:id and /v1/sessions/:owner/:name/tunnel coexist and
// each reaches its own handler (measured, TestTunnelIsNotASessionId).
//
// Both stay UNTYPED, for two different reasons. The tunnel structurally cannot
// be typed: it hijacks the connection, and a typed handler is handed a
// context.Context and no request to hijack. Opening a session could be typed,
// but it mints an object.Session whose own collection is still the enveloped
// /v1/*-session surface — one value answered in two shapes is worse than
// either, so it converts with that noun.
func registerTunnel(app *zip.App) {
	app.Post("/v1/assets/:owner/:name/sessions", h((*controllers.ApiController).OpenSession))
	app.Get("/v1/sessions/:owner/:name/tunnel", h((*controllers.ApiController).Tunnel))
}
