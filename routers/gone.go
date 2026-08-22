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
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// An address visor no longer serves says so, and says where the thing went.
//
// Each retired address put the VERB in the path — get-assets, add-asset,
// update-asset — spelling with a hyphen what the method already says, so a
// client held one URL per verb and the address moved when the operation did.
// The successor is the RESOURCE, and which operation you meant is the method
// you send.
//
// Simply ceasing to exist is worse than never having existed: a 404 says "never
// heard of it", which sends a caller hunting for a typo it will not find. So a
// retired address answers 410 Gone (RFC 9110, section 15.5.11) and names its
// replacement in a Link header with rel="successor-version" (RFC 5829), beside
// Deprecation (RFC 9745) and Sunset (RFC 8594).
//
// This file is the MECHANISM and holds no addresses. The tables live one file
// per family — gone_assets.go and its siblings — so they merge without
// conflict and a reader finds a family's retirements next to that family's
// routes.

// retire answers every address in successor. It takes no store because it reads
// none: no principal, no lookup, no proxy to the successor. Doing any of that
// would make a retirement a third spelling of the resource; saying where it
// went is the whole job.
//
// Registered through [zip.Undeclared], so these addresses SERVE and are absent
// from App.Declaration and therefore from every projection built from it — the
// OpenAPI document, the MCP tool list, the CLI, the by-name call plane. That
// matters because each answers EVERY method: 410 is a statement about the
// target resource, and a caller that sent the wrong verb still needs the
// successor, so publishing them would be one operation per method per address,
// most of them calls that never existed.
//
// Call it AHEAD of the filter chain. A retirement notice behind authorization
// answers 403, and a caller that gets 403 learns nothing about where its
// address went — which is the only reason these entries exist.
func retire(on zip.OpTarget, successor map[string][]string) {
	u := zip.Undeclared(on)
	for path, to := range successor {
		u.All(path, gone(to))
	}
}

// gone is the ONE handler, built once per address over that address's row.
func gone(to []string) zip.Handler {
	links := make([]string, len(to))
	for i, s := range to {
		links[i] = "<" + s + `>; rel="successor-version"`
	}
	link := strings.Join(links, ", ")

	return func(c *zip.Ctx) error {
		// Both stamps are NOW, which is the honest reading rather than a
		// placeholder. Sunset is when the address stops responding and this one
		// already has; RFC 8594, section 3 says a timestamp in the past reads as
		// the present, so now is that rule's fixed point. Deprecation takes the
		// same instant because RFC 9745, section 4 requires Sunset not to precede
		// it. A literal date would be a constant to keep true, and it would say
		// the address is going rather than gone.
		now := time.Now()
		c.SetHeader("Link", link)
		c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
		c.SetHeader("Sunset", now.UTC().Format(http.TimeFormat))
		return c.JSON(http.StatusGone, notice{Successor: to})
	}
}

// notice is the body: where the thing went, rendered from the same row the Link
// header carries so the two cannot disagree.
type notice struct {
	Successor []string `json:"successor"`
}
