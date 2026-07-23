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

package routers

import (
	"io/fs"
	"mime"
	"os"
	"path"
	"strings"

	"github.com/zap-proto/zip"
)

// Static roots. os.DirFS + fs.ValidPath make every read traversal-safe: a
// cleaned "../" escape is rejected fail-closed. The SPA (web/build) is the
// default; /swagger serves the OpenAPI UI from its own root.
var (
	webFS     = os.DirFS("web/build")
	swaggerFS = os.DirFS("swagger")
)

// TransparentStatic is the FIRST request filter after recover/CORS. It replaces
// Beego's BeforeRouter static filter one-for-one: a /v1/ request threads through
// (c.Next()) to the tenant/authz/record chain and the API routes; anything else
// is served from the web build with an index.html SPA fallback (or from the
// swagger root), short-circuiting the chain so no static asset is ever
// authz-gated or audit-recorded.
func TransparentStatic(c *zip.Ctx) error {
	urlPath := c.Path()
	if strings.HasPrefix(urlPath, "/v1/") {
		return c.Next()
	}

	if strings.HasPrefix(urlPath, "/swagger") {
		return serveFrom(c, swaggerFS, strings.TrimPrefix(urlPath, "/swagger"), "")
	}
	return serveFrom(c, webFS, urlPath, "index.html")
}

// serveFrom serves urlPath from fsys. A miss falls back to fallback (the SPA
// shell) when set, else 404. Content-Type is set by extension.
func serveFrom(c *zip.Ctx, fsys fs.FS, urlPath, fallback string) error {
	name := strings.TrimPrefix(urlPath, "/")
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}

	data, served := readStatic(fsys, name)
	if !served {
		if fallback == "" {
			return c.String(404, "not found")
		}
		name = fallback
		if data, served = readStatic(fsys, name); !served {
			return c.String(404, "not found")
		}
	}

	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		c.SetHeader("Content-Type", ct)
	}
	return c.Bytes(200, data)
}

func readStatic(fsys fs.FS, name string) ([]byte, bool) {
	if !fs.ValidPath(name) {
		return nil, false
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, false
	}
	return data, true
}
