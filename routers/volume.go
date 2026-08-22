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

// registerVolume declares the VOLUME noun — block storage an org owns. Seven
// TYPED ops, so it is in the registry every projection reads (OpenAPI, MCP, CLI,
// the by-name call plane) rather than only on the wire.
//
// The addresses it replaced are retired, not deleted: see registerGoneVolumes.
//
// Attachment is a SUB-RESOURCE rather than a field on the volume, because
// detaching has to be expressible without a sentinel value — the reasoning is
// written where the handlers are (controllers/volume.go). Size is a field, so it
// is PATCH on the volume itself and not an address of its own.
func registerVolume(app *zip.App) {
	zip.Get(app, "/v1/volumes", controllers.ListVolumes,
		zip.WithSummary("List the caller org's volumes"),
		zip.WithOperationID("listVolumes"),
		zip.WithTags("Volume"),
	)
	zip.Post(app, "/v1/volumes", controllers.CreateVolume,
		zip.WithSummary("Provision a block volume"),
		zip.WithOperationID("createVolume"),
		zip.WithTags("Volume"),
	)
	zip.Get(app, "/v1/volumes/:id", controllers.GetVolume,
		zip.WithSummary("Read one of the caller org's volumes"),
		zip.WithOperationID("getVolume"),
		zip.WithTags("Volume"),
	)
	zip.Patch(app, "/v1/volumes/:id", controllers.ResizeVolume,
		zip.WithSummary("Grow a volume"),
		zip.WithOperationID("resizeVolume"),
		zip.WithTags("Volume"),
	)
	zip.Delete(app, "/v1/volumes/:id", controllers.DeleteVolume,
		zip.WithSummary("Destroy a volume"),
		zip.WithOperationID("deleteVolume"),
		zip.WithTags("Volume"),
	)
	zip.Put(app, "/v1/volumes/:id/attachment", controllers.AttachVolume,
		zip.WithSummary("Attach a volume to a machine"),
		zip.WithOperationID("attachVolume"),
		zip.WithTags("Volume"),
	)
	zip.Delete(app, "/v1/volumes/:id/attachment", controllers.DetachVolume,
		zip.WithSummary("Detach a volume from its machine"),
		zip.WithOperationID("detachVolume"),
		zip.WithTags("Volume"),
	)
}
