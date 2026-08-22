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
	"net/http"
	"strconv"
	"time"

	"github.com/zap-proto/zip"
)

// goneCatalog is the catalog-and-account family's retirement table: an address
// visor used to serve, and the address that serves it now. It is a table rather
// than a handler per address because the successor has to reach the caller twice
// — once in a Link header, once in the body — and two literals is how those come
// to disagree.
//
// Each family keeps its own table in its own file so a later step can merge them
// into one without a conflict per family.
var goneCatalog = map[string]string{
	// A GPU size is a size. The old address answered ListSizes with a predicate
	// applied — same element type, strict subset — so one collection had two
	// doors and a caller had to learn which one held GPUs. The predicate is now
	// the filter: SizeInfo.HasGPU, asked for as ?gpu.
	"/v1/gpus": "/v1/sizes?gpu=true",
	// The account is a resource, and reading it is what GET means.
	"/v1/get-account": "/v1/account",
}

// retireCatalog serves this family's retired addresses.
//
// Three things make a retirement legible to a client rather than merely broken:
//
//   - 410 Gone (RFC 9110 15.5.11) — the address is not missing, it is finished,
//     and it answers every method because 410 is a statement about the target
//     resource and a caller who also sent the wrong verb still needs to be told
//     where the resource went.
//   - the successor by name, in a Link header (RFC 5829) and in the body, both
//     read from the same table row.
//   - Deprecation (RFC 9745) and Sunset (RFC 8594), both stamped NOW, because
//     the address is gone rather than going. A future Sunset would tell a client
//     it has until then, and it does not.
//
// They are registered on zip.Undeclared, so they SERVE and appear in no
// projection built from the declaration — the OpenAPI document, the MCP tool
// list, the CLI, the generated SDKs. Publishing them would put one dead
// operation per method per address into the customer contract, most of them
// calls that never existed.
//
// It is called AHEAD of the filter chain, next to health and for a related
// reason: a 410 carries no data, so there is nothing to authorize, and gating it
// would answer 403 to the anonymous caller who most needs to be told the address
// moved. It also keeps a dead address out of the audit log, which records what
// happened to visor's data and not who knocked on a door that is bricked up.
func retireCatalog(app *zip.App) {
	r := zip.Undeclared(app)
	for address, successor := range goneCatalog {
		r.All(address, func(c *zip.Ctx) error {
			now := time.Now().UTC()
			c.SetHeader("Link", "<"+successor+`>; rel="successor-version"`)
			c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
			c.SetHeader("Sunset", now.Format(http.TimeFormat))
			return zip.Errorf(http.StatusGone, "%s is gone; use %s", address, successor).
				With(map[string]any{"successor": successor})
		})
	}
}
