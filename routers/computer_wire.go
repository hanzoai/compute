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

	"github.com/hanzoai/compute/controllers"
)

// registerComputer declares one machine's COMPUTE op. It is TYPED so the
// address is in the registry every projection reads, and wrapped with the
// controller's raw response writer so hanzod's status, Content-Type and body
// survive byte-for-byte on REST.
func registerComputer(app *zip.App) {
	zip.Post(app, "/v1/machines/:owner/:name/computer", controllers.Computer,
		zip.WithSummary("Forward a computer-use operation to a machine over the Zero Trust fabric"),
		zip.WithOperationID("machineComputer"),
		zip.WithTags("Compute"),
	)
}
