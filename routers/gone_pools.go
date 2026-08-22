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

// GonePools is the node-pool half of the addresses visor no longer serves.
//
// Each one put the operation in the path — get-node-pool, scale-node-pool —
// saying with a hyphen what the method already says, so a client held six URLs
// for one thing and the address changed when the operation did. The successor is
// the RESOURCE, /v1/k8s/pools, and which of the six you meant is the method you
// send and whether you name an item under it.
//
// An address that simply stops existing is worse than one that never did. A 404
// says "never heard of it", which sends a caller looking for a typo it will not
// find. So each of these answers 410 Gone (RFC 9110, section 15.5.11) and names
// its replacement in a Link header with rel="successor-version" (RFC 5829),
// beside Deprecation (RFC 9745) and Sunset (RFC 8594).
//
// The value is a list because a verb that meant two things splits when it
// becomes a resource, and RFC 5829, section 3.6 admits more than one
// successor-version link for exactly that. None of these six split.
//
// One map per family, so the merge into one table is a concatenation.
var GonePools = map[string][]string{
	"/v1/get-node-pools":   {"/v1/k8s/pools"},
	"/v1/get-node-pool":    {"/v1/k8s/pools"},
	"/v1/create-node-pool": {"/v1/k8s/pools"},
	"/v1/update-node-pool": {"/v1/k8s/pools"},
	"/v1/delete-node-pool": {"/v1/k8s/pools"},
	// scale was not a resource at all: what it changed is `count`, a field the
	// pool publishes, so it is a PUT on the pool.
	"/v1/scale-node-pool": {"/v1/k8s/pools"},
}

// routeGonePools answers every retired node-pool address on r. It takes no store
// because it reads none: the table is the whole handler. Reading a principal, or
// proxying to the successor, would make this a third spelling — saying WHERE the
// thing went is what makes it a retirement.
//
// All, not Get and Post: 410 is a statement about the target resource (RFC 9110,
// section 15.5.11), so the address is gone whatever method reaches it. Naming
// methods here would leave a caller that sent the wrong one with a 405 and no
// successor.
//
// Registered on zip.Undeclared, so these addresses SERVE and are not part of the
// contract. Each answers every method, so publishing them would be one operation
// per method per address — a document listing dead endpoints, which is a document
// nobody can read.
//
// The caller registers this AHEAD of the filter chain, which is the whole point
// of a retirement notice: behind authorization it answers 403, and a caller that
// gets 403 learns nothing about where its address went.
//
// answer is local rather than a package function so this file names exactly two
// things, both of them this family's — the map and its registration — and the
// step that merges the family tables into one lifts one copy of it out.
func routeGonePools(r zip.Router) {
	// Built once per address over that address's own row, so the Link header and
	// the body carry the same successor and cannot disagree.
	answer := func(to []string) zip.Handler {
		links := make([]string, len(to))
		for i, s := range to {
			links[i] = "<" + s + `>; rel="successor-version"`
		}
		link := strings.Join(links, ", ")

		return func(c *zip.Ctx) error {
			// Both stamps are NOW, and that is the honest reading rather than a
			// placeholder. Sunset is when the address becomes unresponsive and this one
			// already is; RFC 8594, section 3 says a timestamp in the past reads as the
			// present, so now is the fixed point of that rule. Deprecation takes the
			// same instant because RFC 9745, section 4 requires Sunset not to precede
			// it. A literal date would be a constant to keep true, and it would say the
			// resource is going rather than gone.
			now := time.Now()
			c.SetHeader("Link", link)
			c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
			c.SetHeader("Sunset", now.UTC().Format(http.TimeFormat))
			return c.JSON(http.StatusGone, map[string][]string{"successor": to})
		}
	}

	u := zip.Undeclared(r)
	for path, to := range GonePools {
		u.All(path, answer(to))
	}
}
