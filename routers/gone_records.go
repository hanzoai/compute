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
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/zap-proto/zip"
)

// goneRecords is the records family's share of visor's retirement table: an
// address that no longer exists, beside the address that replaced it. The row is
// the ONLY statement of the successor — the Link header and the body are both
// rendered from it, so a caller reading either gets the same answer.
//
// Every left-hand address spelled the OPERATION in the path, which gave one
// resource seven names and moved a client's URL whenever its verb changed. The
// right-hand side names the resource once and lets the method carry the verb.
var goneRecords = map[string]string{
	"/v1/get-records":   "/v1/records",
	"/v1/add-record":    "/v1/records",
	"/v1/get-record":    "/v1/records/:owner/:name",
	"/v1/update-record": "/v1/records/:owner/:name",
	"/v1/delete-record": "/v1/records/:owner/:name",
	"/v1/commit-record": "/v1/records/:owner/:name/block",
	"/v1/query-record":  "/v1/records/:owner/:name/block",
}

// registerGone installs a retirement table on the app.
//
// Every address answers for EVERY method, because 410 is a statement about the
// target resource (RFC 9110 §15.5.11): a caller that also had the verb wrong
// still needs to be told where the resource went, and answering 405 there sends
// it looking for a method that does not exist either.
//
// They go on zip.Undeclared, so they SERVE and are absent from App.Declaration
// and every projection built from it. Publishing them would put one operation
// per method per address into the OpenAPI document, the MCP tool list, the CLI
// and the generated SDKs — dead endpoints in the customer contract, most of them
// calls that never existed.
//
// Registered AHEAD of the filter chain (see Route), for the reason health is: an
// address that is gone is gone for everyone. Behind the authorizer, the same
// request answers 410 with a credential and 403 without one, which tells an
// anonymous caller its URL is forbidden when in fact it is retired.
func registerGone(app *zip.App, table map[string]string) {
	r := zip.Undeclared(app)
	for address, successor := range table {
		r.All(address, gone(successor))
	}
}

// gone answers 410 and names the successor three ways from ONE value: the Link
// relation a client follows (RFC 5829 rel="successor-version"), and the
// `successor` member of the RFC 9457 problem document a human reads. Deprecation
// (RFC 9745, an sf-date) and Sunset (RFC 8594, an HTTP-date) are both stamped
// NOW, from a single instant, because the address is gone rather than going —
// two clocks would let the two headers disagree about the same fact.
func gone(successor string) zip.Handler {
	return func(c *zip.Ctx) error {
		now := time.Now().UTC()
		c.SetHeader("Link", fmt.Sprintf("<%s>; rel=\"successor-version\"", successor))
		c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
		c.SetHeader("Sunset", now.Format(http.TimeFormat))
		return zip.Errorf(http.StatusGone, "%s is gone; the resource is at %s", c.Path(), successor).
			With(map[string]any{"successor": successor})
	}
}
