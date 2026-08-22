// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package controllers

import (
	"context"

	"github.com/hanzoai/visor/object"
)

// The WHITELABEL of a hostname: the branding visor serves under it — name,
// logo, favicon, colour, support and docs links, and the org a brand's console
// is scoped to (object/whitelabel.go). ONE typed op, so the shape a caller sees
// is the shape the code states, and the address names the resource rather than
// the act of fetching it: GET /v1/whitelabel, where it was GET
// /v1/get-whitelabel (retired — routers/gone_whitelabel.go).
//
// It is public-read, and has to be: the console asks for its branding before it
// asks who the visitor is, so an unauthenticated answer is the whole point.
// authz/authz.go carries the policy line that admits it.

// Host is the whole input: the hostname whose whitelabel is being asked for.
//
// DECLARED rather than read off the request, and that is what makes this op
// mean the same thing over HTTP and by name. Over the wire it is the browser's
// Host header, so a caller sends nothing and gets the brand of the site it is
// on; in-process — zip.Here, an MCP tool, a CLI flag — it is the hostname the
// caller is asking about, which a handler reaching into a request could not
// have been given at all.
type Host struct {
	Host string `json:"-" header:"Host"`
}

// GetWhitelabel returns the branding one hostname serves, falling back to the
// Hanzo default for a hostname with no configuration of its own.
//
// Response: {"appName": "Lux Visor", "primaryColor": "#0066ff", "orgFilter": "lux"}
func GetWhitelabel(_ context.Context, in *Host) (*object.WhitelabelConfig, error) {
	return object.GetWhitelabelConfig(in.Host), nil
}
