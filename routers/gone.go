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

// Retirement: the addresses visor no longer serves.
//
// Each put the verb in the path — get-providers, add-provider, delete-provider —
// saying with a hyphen what the method already says. The successor is the
// RESOURCE, and which verb you meant is the method you send.
//
// An address that simply stops existing is worse than one that never did. A 404
// says "never heard of it", which sends a caller looking for a typo it will not
// find. So each answers 410 Gone (RFC 9110, section 15.5.11) and names its
// replacement in a Link header with rel="successor-version" (RFC 5829), beside
// Deprecation (RFC 9745) and Sunset (RFC 8594).
//
// ONE handler, and it reads nothing but the table it was built from: no store,
// no principal, no proxy to the successor. Doing any of that would make this a
// third spelling of the resource. Saying where the thing went is what makes it a
// retirement.
//
// The tables live one per family (gone_providers.go, …) and are handed here at
// registration, so a family retires an address by writing a row.

// retire answers every address in table on r.
//
// zip.Undeclared, because these addresses SERVE and are not part of the
// contract. A document that lists them lists dead endpoints, and because each
// answers every method, publishing them would be one operation per method per
// address — most of them calls that never existed.
//
// All, not Get and Post: 410 is a statement about the target resource, so the
// address is gone whatever method reaches it. Naming methods here would leave a
// caller that sent the wrong one with a 405 and no successor.
func retire(r zip.Router, tables ...map[string][]string) {
	u := zip.Undeclared(r)
	for _, table := range tables {
		for path, to := range table {
			u.All(path, retired(to))
		}
	}
}

// retired is the one handler, built once per address over that address's row.
func retired(to []string) zip.Handler {
	links := make([]string, len(to))
	for i, s := range to {
		links[i] = "<" + s + `>; rel="successor-version"`
	}
	link := strings.Join(links, ", ")

	return func(c *zip.Ctx) error {
		// Both stamps are NOW, and that is the honest reading rather than a
		// placeholder. Sunset is when the address becomes unresponsive and this one
		// already is; RFC 8594, section 3 reads a timestamp in the past as the
		// present, so now is the fixed point of that rule. Deprecation takes the
		// same instant because RFC 9745, section 4 requires Sunset not to precede
		// it. A literal date would be a constant to keep true, and it would say the
		// resource is going rather than gone.
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
