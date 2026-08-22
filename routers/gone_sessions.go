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

// The session addresses this service no longer serves.
//
// Each put the verb in the path — get-session, update-session, stop-session —
// saying with a hyphen what the method already says, and each carried the thing
// it addressed in a `?id=owner/name` query instead. The successor is the
// RESOURCE, and which of the verbs you meant is the method you send.
//
// An address that simply stops existing is worse than one that never did: 404
// says "never heard of it", which sends a caller hunting for a typo it will not
// find. So each answers 410 Gone (RFC 9110, section 15.5.11) and names its
// replacement in a Link header with rel="successor-version" (RFC 5829), beside
// Deprecation (RFC 9745) and Sunset (RFC 8594).
//
// The four CRUD verbs name the COLLECTION, because that is the noun they
// collapsed onto and the item address hangs off it — which of the five you
// wanted is the method. The two connection verbs name the sub-resource itself,
// written as an RFC 6570 template, because no method on /v1/sessions reaches it:
// tearing a live tunnel down is not a way of writing the record.
var goneSessions = map[string][]string{
	"/v1/get-sessions":   {"/v1/sessions"},
	"/v1/get-session":    {"/v1/sessions"},
	"/v1/update-session": {"/v1/sessions"},
	"/v1/delete-session": {"/v1/sessions"},
	"/v1/start-session":  {"/v1/sessions/{owner}/{name}/connection"},
	"/v1/stop-session":   {"/v1/sessions/{owner}/{name}/connection"},
}

// retireSessions answers every retired session address on app. It reads no
// store, no principal, and never proxies to the successor: doing any of those
// would make this a third spelling. Saying where the thing went is what makes it
// a retirement.
//
// Undeclared, so these addresses SERVE and are absent from App.Declaration and
// from every projection built from it. Each answers EVERY method, because 410 is
// a statement about the target resource and a caller who sent the wrong verb
// still needs the successor — published, that would be one operation per method
// per address, most of them calls that never existed.
//
// Registered AHEAD of the filter chain for the same reason IAM registers its own
// on the public group: behind authorization a retirement notice answers 403, and
// a caller that gets 403 learns nothing about where its address went, which is
// the whole reason these entries exist.
func retireSessions(app *zip.App) {
	// The body: where the thing went, rendered from the same row the Link header
	// carries, so the two cannot disagree.
	type notice struct {
		Successor []string `json:"successor"`
	}

	u := zip.Undeclared(app)
	for path, to := range goneSessions {
		links := make([]string, len(to))
		for i, s := range to {
			links[i] = "<" + s + `>; rel="successor-version"`
		}
		link := strings.Join(links, ", ")
		body := notice{Successor: to}

		u.All(path, func(c *zip.Ctx) error {
			// Both stamps are NOW, which is the honest reading rather than a
			// placeholder: Sunset is when the address becomes unresponsive and this
			// one already is (RFC 8594, section 3 reads a past timestamp as the
			// present), and Deprecation takes the same instant because RFC 9745,
			// section 4 requires Sunset not to precede it. A literal date would be a
			// constant to keep true, and it would say the resource is going rather
			// than gone.
			now := time.Now()
			c.SetHeader("Link", link)
			c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
			c.SetHeader("Sunset", now.UTC().Format(http.TimeFormat))
			return c.JSON(http.StatusGone, body)
		})
	}
}
