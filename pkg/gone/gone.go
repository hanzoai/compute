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

// Package gone answers an address that has been retired.
//
// A retired address is not missing and it is not moved — 404 says the server
// knows nothing about it, and a redirect says to try again over there, which a
// client library will do without ever telling anyone. 410 (RFC 9110 §15.5.11)
// says the resource is gone and the condition is permanent, which is the one
// answer a caller cannot mistake for a network fault.
//
// Gone alone leaves the caller with nowhere to go, so the answer also NAMES its
// replacement, three ways that cannot disagree because all three render from
// one [Successor] row:
//
//	Deprecation: @1771200000                       RFC 9745 — a structured Date
//	Sunset: Wed, 19 Aug 2026 00:00:00 GMT          RFC 8594 — an HTTP-date
//	Link: </v1/volumes>; rel="successor-version"   RFC 8288 + RFC 5829 §3.5
//
// Both stamps are NOW rather than a date in the future. A Sunset a caller can
// read as "still time" is a lie once the address answers 410, and the pair of
// them read together says the only true thing: this happened, it is not going
// to happen.
//
// The body is the RFC 9457 problem document every other refusal in this service
// answers with, carrying the successor as an extension member — so a client
// that never reads response headers still learns where the resource went.
package gone

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zap-proto/zip"
)

// Successor is where an address went: the ONE row a retirement is written from.
// The Link header carries its Path, the problem document carries both halves,
// and neither is written from anywhere else, so the header and the body cannot
// name different places.
//
// Path is a URI template in the RFC 6570 spelling ({id}), not the router's
// (:id): it is a statement to a client about an address, and the client has
// never heard of fiber.
type Successor struct {
	// Method is the verb that now does what the retired address did. It rides
	// the body rather than the Link header because a link names a target, not a
	// call — and the whole point of the move is that the verb is the method.
	Method string
	// Path is the address that now serves the resource.
	Path string
}

// Serve registers one 410 answer per entry of table on r.
//
// r MUST come from [zip.Undeclared]. A retired address answers EVERY method —
// 410 is a statement about the target resource, and a caller who sent the wrong
// verb still needs the successor — so a declared retirement would publish one
// operation per method per address: a customer contract listing dead endpoints,
// which is a contract nobody can read.
func Serve(r zip.Router, table map[string]Successor) {
	for addr, to := range table {
		r.All(addr, answer(to))
	}
}

// answer writes the three stamps and refuses with the problem document.
//
// The headers are set on the context and the refusal is RETURNED, because zip's
// error handler writes only the status and the body — so a header put here
// survives it, and the refusal stays the one shape this service refuses in.
func answer(to Successor) zip.Handler {
	successor := map[string]any{"method": to.Method, "path": to.Path}
	link := "<" + to.Path + `>; rel="successor-version"`
	return func(c *zip.Ctx) error {
		now := time.Now().UTC()
		c.SetHeader("Deprecation", "@"+strconv.FormatInt(now.Unix(), 10))
		c.SetHeader("Sunset", now.Format(http.TimeFormat))
		c.SetHeader("Link", link)
		// The address the caller used is deliberately not echoed back: they
		// already hold it, and repeating it is how two refusals at two addresses
		// come to differ in ways a test then has to be loosened to accept.
		return zip.Errorf(http.StatusGone, "this address is retired; the resource is now at %s %s",
			to.Method, to.Path).With(map[string]any{"successor": successor})
	}
}
