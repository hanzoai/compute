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

// Package visor exposes visor's in-process bootstrap so a parent binary
// (github.com/hanzoai/cloud) can mount visor's /v1 surface in the same address
// space — the same routers, controllers and filters the standalone server runs,
// minus the listener.
//
// There is ONE boot path. Both cmd main() and the embedded cloud mount call
// Bootstrap; the only difference is who owns the listener (main.go calls
// app.Listen; the cloud mount serves Handler() behind its own listener).
package visor

import (
	"fmt"
	"net/http"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/authz"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/routers"
	"github.com/hanzoai/visor/task"
	"github.com/hanzoai/visor/util"
)

// initState runs visor's stateful process initialization — DB adapter, authz,
// IP/UA parsers and background tickers — the half of boot that is independent of
// the HTTP framework. Shared by the standalone server and the embedded mount so
// the two can never drift.
func initState() {
	object.InitAdapter()
	authz.InitAuthz()
	util.InitIpDb()
	util.InitParser()

	task.NewTicker().SetupTicker()
}

// Bootstrap runs visor's full in-process initialization and returns the wired
// zip App — the ONE boot path. The caller owns the listener: cmd main() calls
// app.Listen; the embedded cloud mount serves Handler().
func Bootstrap() *zip.App {
	initState()

	// MCP is auto-derived from TYPED zip ops; visor has none (its handlers are
	// classic controllers), so disable the surface outright — one fewer thing to
	// defend.
	app := zip.New(zip.Config{
		AppName:               "visor",
		DisableStartupMessage: true,
		MCP:                   zip.MCPConfig{Disabled: true},
	})
	routers.Route(app)
	return app
}

// Handler boots visor in-process and returns its HTTP handler for a parent
// binary to mount behind its own listener. Any panic from the underlying boot
// is recovered and returned as an error so a parent's mount fails cleanly
// (fail-closed) rather than crashing the whole process.
func Handler() (h http.Handler, err error) {
	defer func() {
		if r := recover(); r != nil {
			h, err = nil, fmt.Errorf("visor.Handler: bootstrap panicked: %v", r)
		}
	}()
	app := Bootstrap()
	if err := app.Build(); err != nil {
		return nil, fmt.Errorf("visor.Handler: %w", err)
	}
	return adaptor.FiberApp(app.Fiber()), nil
}
