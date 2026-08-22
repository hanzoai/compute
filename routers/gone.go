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

// A RETIRED ADDRESS ANSWERS, AND IT NAMES WHAT REPLACED IT.
//
// 404 is not an answer for a path that moved: it is what a typo says, so a
// caller holding a stale address cannot tell a rename from a mistake and has
// nowhere to look next. 410 (RFC 9110 15.5.11) says the address is gone and
// means it, and the successor rides beside it — a Link naming the replacement
// under rel="successor-version" (RFC 5829), with Deprecation (RFC 9745) and
// Sunset (RFC 8594). Both stamps are NOW, because the address is gone rather
// than going: there is no future date to warn about.
//
// The body carries the same successor the headers do, rendered from ONE row, so
// a client that reads headers and a client that reads JSON are never told two
// different things.

// registerGone mounts every retired address in the estate's retirement table.
//
// It is called AHEAD of the filter chain, beside health, and the position is the
// design. 410 is a statement about the target resource, not about the caller,
// and no credential admits a resource that does not exist — so there is nothing
// to authorize. Behind the policy engine a caller holding a stale address is
// told 403 and never learns the successor: the authorization answer hides the
// routing one.
func registerGone(app *zip.App) {
	// Undeclared (zip v1.33.1+): these routes SERVE but are absent from
	// App.Declaration, and so from every projection built from it — the OpenAPI
	// document, the MCP tool list, the CLI, the by-name call plane. That is
	// load-bearing rather than tidy. A retired address answers EVERY method,
	// because a caller who also got the verb wrong still needs the successor, so
	// publishing them would put one dead operation per method per address into
	// the contract customers read.
	r := zip.Undeclared(app)
	for _, table := range []map[string]string{goneMachines} {
		for path, successor := range table {
			r.All(path, gone(successor))
		}
	}
}

// gone answers 410 for one retired address, naming successor in the headers and
// in the body from the single value it was handed.
//
// The stamps are read once, here, rather than per request: they say when this
// build declared the address gone, which does not change while it is running,
// and a value that drifts request to request is one no cache can hold.
func gone(successor string) zip.Handler {
	link := "<" + successor + `>; rel="successor-version"`
	now := time.Now().UTC()
	deprecation := "@" + strconv.FormatInt(now.Unix(), 10)
	sunset := now.Format(http.TimeFormat)
	return func(c *zip.Ctx) error {
		c.SetHeader("Link", link)
		c.SetHeader("Deprecation", deprecation)
		c.SetHeader("Sunset", sunset)
		return zip.Errorf(http.StatusGone, "this address is gone; the resource is at %s", successor).
			With(map[string]any{"successor": successor})
	}
}
