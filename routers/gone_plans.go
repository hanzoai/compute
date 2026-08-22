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

import "github.com/hanzoai/visor/pkg/gone"

// gonePlans is the plan catalog's retirement table: the five addresses that
// carried the OPERATION in the path, each naming the collection that carries
// the resource now.
//
// All five name /v1/plans and not an item address, because the item is reached
// by appending a plan's name to the collection and a Link header cannot point
// at a template. A client holding get-plan?owner=X&name=Y is told where the
// catalog is; it already has the name.
var gonePlans = gone.Table{
	"/v1/get-plans":   "/v1/plans",
	"/v1/get-plan":    "/v1/plans",
	"/v1/add-plan":    "/v1/plans",
	"/v1/update-plan": "/v1/plans",
	"/v1/delete-plan": "/v1/plans",
}
