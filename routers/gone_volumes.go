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

	"github.com/hanzoai/visor/pkg/gone"
)

// goneVolumes is the volume family's share of visor's retirement table: the
// seven addresses that spelled the operation in the path, each pointing at the
// one method+address that now serves it.
//
// It is a family-scoped map rather than lines in a shared file so that the
// families being renamed in parallel do not write the same lines; merging them
// into one table later is a union of maps and nothing else.
//
// Paths are RFC 6570 templates ({id}), which is what a client and an OpenAPI
// document both read. The router spells the same address :id.
var goneVolumes = map[string]gone.Successor{
	"/v1/get-volumes":   {Method: "GET", Path: "/v1/volumes"},
	"/v1/get-volume":    {Method: "GET", Path: "/v1/volumes/{id}"},
	"/v1/create-volume": {Method: "POST", Path: "/v1/volumes"},
	"/v1/delete-volume": {Method: "DELETE", Path: "/v1/volumes/{id}"},
	"/v1/attach-volume": {Method: "PUT", Path: "/v1/volumes/{id}/attachment"},
	"/v1/detach-volume": {Method: "DELETE", Path: "/v1/volumes/{id}/attachment"},
	"/v1/resize-volume": {Method: "PATCH", Path: "/v1/volumes/{id}"},
}

// registerGoneVolumes mounts the retired volume addresses. They SERVE — a
// caller still on one gets 410 and the successor rather than a 404 it would
// read as an outage — and they are absent from App.Declaration, so no
// projection built from it (OpenAPI, MCP, CLI, SDK) carries a dead endpoint.
func registerGoneVolumes(app *zip.App) {
	gone.Serve(zip.Undeclared(app), goneVolumes)
}
