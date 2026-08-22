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

// Package gone answers for an address the service no longer serves, and names
// the one that serves its resource now.
//
// A retired address is not a 404. 404 says "there is nothing here", which a
// client reads as a typo or an outage and RETRIES; 410 says "there was
// something here and it is finished" (RFC 9110 §15.5.11), which is a fact a
// client can act on once. Acting on it needs the successor, so the answer
// carries three registered stamps rather than prose a human has to read:
//
//	Link: </v1/plans>; rel="successor-version"   RFC 5829 — where it went
//	Deprecation: @1755000000                     RFC 9745 — an sf-date, "@"+unix
//	Sunset: Mon, 01 Jan 2026 00:00:00 GMT        RFC 8594 — http.TimeFormat
//
// Both stamps read NOW, deliberately: the address is gone, not going. A future
// Sunset would tell a client it has until then, which is a promise nothing here
// keeps.
//
// The header and the body render the SAME row (see [retired]), so the two
// cannot disagree — a client that reads headers and a person reading a curl are
// told one thing.
package gone

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zap-proto/zip"
)

// Table is a retirement: each address that is gone, naming the address that
// serves its resource now.
//
// The successor is a URI a client can fetch, never a URI template — an item
// address is reached by appending a name to its collection, and a Link header
// whose target is "/v1/plans/{name}" points at nothing. So a retired item verb
// names its COLLECTION, which is both true and dereferenceable.
type Table map[string]string

// Answer is what a retired address returns: the address that was asked for, and
// the address to ask instead.
type Answer struct {
	Gone      string `json:"gone"`
	Successor string `json:"successor"`
}

// Serve registers every entry of t as a retired address on r.
//
// They are registered on [zip.Undeclared], so they SERVE but stay out of
// App.Declaration and therefore out of every projection built from it — the
// OpenAPI document, the MCP tool list, the CLI, the by-name call plane. That
// matters because a retired address answers EVERY method: 410 is a statement
// about the target resource, and a caller who sent the wrong verb still needs
// the successor. Published, six dead addresses would be forty-eight operations
// in the customer contract, most of them calls that never existed.
func Serve(r zip.OpTarget, t Table) {
	u := zip.Undeclared(r)
	for from, to := range t {
		g := retired{from: from, to: to}
		u.All(from, g.serve)
	}
}

// retired is ONE retirement. The headers and the body are both rendered from
// it, which is what makes them agree by construction rather than by review.
type retired struct{ from, to string }

func (g retired) serve(c *zip.Ctx) error {
	now := time.Now().UTC()
	c.SetHeader("Link", "<"+g.to+`>; rel="successor-version"`)
	c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
	c.SetHeader("Sunset", now.Format(http.TimeFormat))
	return c.JSON(http.StatusGone, Answer{Gone: g.from, Successor: g.to})
}
