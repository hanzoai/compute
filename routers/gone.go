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

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zap-proto/zip"
)

// Addresses compute used to serve, and the resource that replaced each.
//
// This is the ONE table. A retirement recorded in two places is two lists to
// keep in agreement, and the one that drifts is the one nobody reads.
//
// The old surface put the operation in the path — /v1/get-asset,
// /v1/attach-volume — so a client held one URL per verb and the address changed
// when the operation did. The resource is the address now and the METHOD says
// what to do with it.
var successor = map[string]string{}

// Retire records that path is gone and names what replaced it.
//
// Called from each family's own file, so a family is added or removed in one
// place and this table follows.
func Retire(path, to string) { successor[path] = to }

// Retired reports whether path is one this package answers for.
func Retired(path string) bool { _, ok := successor[path]; return ok }

// registerGone answers every retired address on r.
//
// UNDECLARED: these serve and are absent from App.Declaration, and so from the
// OpenAPI document, the MCP tool list, the CLI and every generated SDK. A
// retired address answers EVERY method, so publishing them would be one
// operation per method per address — dead endpoints in a contract that is
// supposed to say what a caller CAN do.
//
// BEFORE THE FILTERS. Registered beside health, ahead of ApiFilter, because a
// caller at a retired address has no live target for a policy to be about: the
// filter would refuse it 403 and the notice — the successor, the stamps — would
// never reach the one client that still needs to read it.
func registerGone(app *zip.App) {
	u := zip.Undeclared(app)
	for path, to := range successor {
		u.All(path, notice(to))
	}
}

// notice is the answer, built once per address over that address's row.
//
// All, not Get and Post: 410 is a statement about the target RESOURCE (RFC 9110
// section 15.5.11), so the address is gone whatever method reaches it. Naming
// methods would leave a caller who sent the wrong one with a 405 and no
// successor — which is the one thing they need.
func notice(to string) zip.Handler {
	link := "<" + to + `>; rel="successor-version"`
	return func(c *zip.Ctx) error {
		// Both stamps are NOW, and that is the honest reading rather than a
		// placeholder: Sunset is when the address stops answering and this one
		// already has. RFC 8594 section 3 reads a past timestamp as the present,
		// and RFC 9745 section 4 wants Sunset not to precede Deprecation, which
		// one instant satisfies.
		now := time.Now()
		c.SetHeader("Link", link)
		c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
		c.SetHeader("Sunset", now.UTC().Format(http.TimeFormat))
		return c.JSON(http.StatusGone, map[string]string{
			// The body carries what the header carries, from the same row, so
			// a reader of either is told the same thing.
			"successor": to,
			"detail":    "this address is gone; the resource is at " + to,
		})
	}
}
