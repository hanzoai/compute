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

package main

import (
	"os"
	"strings"
	"testing"
)

// banned are module paths this service must never link, each with the reason.
// Two things land here, and the mechanism is the same for both.
//
// UNPATCHABLE. github.com/hanzoai/iam-v1 is ARCHIVED on GitHub: a repo that
// cannot take a push cannot take a security fix, so no deployed binary may
// depend on one — the dependency is unpatchable by construction, whatever the
// code inside says. It is the Casdoor lineage's old home; its ROOT package was
// a second copy of the Casdoor Go SDK, 67 of 73 files byte-identical to
// github.com/hanzoai/iamsdk/v2/iamsdk, which is live and tagged. visor required
// it DIRECTLY while also linking the v2 lineage through cloud — one binary, both
// implementations. The SDK is the one that stays.
//
// SECOND WEB FRAMEWORK. visor serves on zip and only zip. Beego is named here
// because visor WAS a Beego service and the rip is the reason this list grew:
// the import is gone, and this is what keeps it gone. The rest never shipped in
// visor and are listed so they cannot arrive — a framework does not have to be
// used to be linked, and a second router reachable from the binary is a second
// place a route can exist. One framework is not a preference about style; it is
// what makes the served route table knowable from one file.
//
// zip's OWN engine (github.com/zap-proto/fiber, github.com/valyala/fasthttp) is
// deliberately absent: zip requires it, so banning it here would ban zip. That
// engine is not a second framework — it is the one framework's underside — and
// whether visor reaches past zip to touch it is a question about visor's source,
// which this file cannot see and does not pretend to.
var banned = map[string]string{
	"github.com/hanzoai/iam-v1": "archived Casdoor fork — use github.com/hanzoai/iamsdk/v2/iamsdk",

	"github.com/beego/beego":              "visor's old framework — it serves on github.com/zap-proto/zip",
	"github.com/astaxie/beego":            "Beego's pre-rename import path — same framework, older address",
	"github.com/gin-gonic/gin":            "second web framework — visor serves on github.com/zap-proto/zip",
	"github.com/labstack/echo":            "second web framework — visor serves on github.com/zap-proto/zip",
	"github.com/gofiber/fiber":            "second web framework — zip's engine is github.com/zap-proto/fiber, reached through zip",
	"github.com/gorilla/mux":              "second router — visor serves on github.com/zap-proto/zip",
	"github.com/go-chi/chi":               "second router — visor serves on github.com/zap-proto/zip",
	"github.com/julienschmidt/httprouter": "second router — visor serves on github.com/zap-proto/zip",
	"github.com/kataras/iris":             "second web framework — visor serves on github.com/zap-proto/zip",
	"github.com/gobuffalo/buffalo":        "second web framework — visor serves on github.com/zap-proto/zip",
	"github.com/revel/revel":              "second web framework — visor serves on github.com/zap-proto/zip",
	"github.com/go-martini/martini":       "second web framework — visor serves on github.com/zap-proto/zip",
	"github.com/urfave/negroni":           "second middleware stack — visor's chain is zip's",
}

// covers reports whether module path f is banned path p, or a package/major
// version beneath it. The boundary matters in both directions: "…/echo" must
// catch "…/echo/v4" and "…/beego" must catch "…/beego/v2", while neither may
// swallow an unrelated module that merely starts with the same letters.
func covers(p, f string) bool {
	return f == p || strings.HasPrefix(f, p+"/")
}

// hit pairs the module that was seen with the reason it is banned. The two
// differ whenever a module is versioned: `github.com/beego/beego/v2` is covered
// by the banned prefix `github.com/beego/beego`, so looking the REPORTED path
// back up in the map finds nothing — and the failure would name the module while
// giving no reason, which is the half of the message a reader actually needs.
type hit struct{ path, why string }

// bannedIn reports every banned module named anywhere in a go.mod. It reads
// go.mod rather than the import graph on purpose: an import scan only catches a
// direct `import`, and the way a banished module comes back is transitively,
// through a dependency's own require. go.mod carries both.
func bannedIn(goMod string) []hit {
	var found []hit
	for _, line := range strings.Split(goMod, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && !strings.Contains(line, "// indirect") {
			line = line[:i]
		}
		for _, f := range strings.Fields(line) {
			for p, why := range banned {
				if covers(p, f) {
					found = append(found, hit{path: f, why: why})
				}
			}
		}
	}
	return found
}

// TestBannedInDetects proves the detector actually fires — a check that cannot
// fail is not a check. The fixture is the require line this repo carried until
// the port off the archived fork.
func TestBannedInDetects(t *testing.T) {
	const fixture = "require (\n\tgithub.com/hanzoai/iam-v1 v1.31.36\n)\n"
	got := bannedIn(fixture)
	if len(got) != 1 || got[0].path != "github.com/hanzoai/iam-v1" || got[0].why == "" {
		t.Fatalf("bannedIn(fixture) = %v, want [github.com/hanzoai/iam-v1]", got)
	}
	// An indirect require is just as load-bearing at link time.
	if got := bannedIn("\tgithub.com/hanzoai/iam-v1 v1.31.36 // indirect\n"); len(got) != 1 {
		t.Errorf("bannedIn(indirect) = %v, want the path reported", got)
	}
	if got := bannedIn("\tgithub.com/hanzoai/iamsdk/v2 v2.2.1\n"); len(got) != 0 {
		t.Errorf("bannedIn(replacement) = %v, want none", got)
	}
}

// TestBannedInCatchesFrameworks is what the beego rip leaves behind. Every line
// is the exact shape a returning framework has — including the MAJORED paths,
// which plain equality misses and which are the only shape that actually occurs,
// since every one of these is past v1.
func TestBannedInCatchesFrameworks(t *testing.T) {
	for _, req := range []string{
		"\tgithub.com/beego/beego/v2 v2.3.4\n",
		"\tgithub.com/astaxie/beego v1.12.3\n",
		"\tgithub.com/gin-gonic/gin v1.10.0 // indirect\n",
		"\tgithub.com/labstack/echo/v4 v4.12.0\n",
		"\tgithub.com/gofiber/fiber/v2 v2.52.5\n",
		"\tgithub.com/go-chi/chi/v5 v5.1.0\n",
		"\tgithub.com/gorilla/mux v1.8.1\n",
	} {
		if got := bannedIn(req); len(got) == 0 {
			t.Errorf("bannedIn(%q) reported nothing — a second framework would link unnoticed", strings.TrimSpace(req))
		}
	}
}

// TestBannedInSpares proves the prefix match does not overreach. Every line here
// is how visor actually serves, and each is one boundary slip away from a banned
// path: zip's engine is a fiber, and the websocket packages sit next to a router
// that is banned by the same first two path elements.
func TestBannedInSpares(t *testing.T) {
	for _, req := range []string{
		"\tgithub.com/zap-proto/fiber/v3 v3.2.1\n",
		"\tgithub.com/zap-proto/zip v1.27.0\n",
		"\tgithub.com/valyala/fasthttp v1.72.0\n",
		"\tgithub.com/fasthttp/websocket v1.5.12\n",
		"\tgithub.com/gorilla/websocket v1.5.4\n",
		"\tgithub.com/gofiber/schema v1.7.1\n",
		"\tgithub.com/gofiber/utils/v2 v2.0.4\n",
	} {
		if got := bannedIn(req); len(got) != 0 {
			t.Errorf("bannedIn(%q) = %v, want none — that is how visor serves", strings.TrimSpace(req), got)
		}
	}
}

// TestNoBannedModules is the standing check on the real manifest.
func TestNoBannedModules(t *testing.T) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, f := range bannedIn(string(b)) {
		t.Errorf("go.mod requires %s — %s", f.path, f.why)
	}
}
