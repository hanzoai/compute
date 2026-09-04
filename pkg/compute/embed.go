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

// Package compute exposes compute's in-process bootstrap so a parent binary
// (github.com/hanzoai/cloud) can mount compute's /v1 surface in the same address
// space — the same routers, controllers and filters the standalone server runs,
// minus the listener.
//
// There is ONE boot path. Both cmd main() and the embedded cloud mount call
// Bootstrap; the only difference is who owns the listener (main.go calls
// app.Listen; the cloud mount serves Handler() behind its own listener).
package compute

import (
	"fmt"
	"net/http"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/authz"
	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/routers"
	"github.com/hanzoai/compute/task"
	"github.com/hanzoai/compute/util"
)

// Name is what compute is called on the fleet, stated once. It is the app name zip
// serves under AND the name its canonical socket is derived from
// (zip.SocketPath(Name)), because those are the same fact: a caller reaches a
// peer BY NAME, so a second spelling of the name is a peer that cannot be found.
const Name = "compute"

// initState runs compute's stateful process initialization — DB adapter, authz,
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

// Bootstrap runs compute's full in-process initialization and returns the wired
// zip App — the ONE boot path. The caller owns the listener: cmd main() calls
// app.Listen; the embedded cloud mount serves Handler().
func Bootstrap() *zip.App {
	initState()

	// MCP is derived from the TYPED ops, so it is worth having now that compute
	// declares one. Its path is routers' to state, because routers is what has to
	// let it past the static filter.
	app := zip.New(zip.Config{
		AppName:               Name,
		DisableStartupMessage: true,
		MCP:                   zip.MCPConfig{Path: routers.MCPPath},
	})
	routers.Route(app)
	return app
}

// Handler boots compute in-process and returns its HTTP handler for a parent
// binary to mount behind its own listener. Any panic from the underlying boot
// is recovered and returned as an error so a parent's mount fails cleanly
// (fail-closed) rather than crashing the whole process.
func Handler() (h http.Handler, err error) {
	defer func() {
		if r := recover(); r != nil {
			h, err = nil, fmt.Errorf("compute.Handler: bootstrap panicked: %v", r)
		}
	}()
	app := Bootstrap()
	if err := app.Build(); err != nil {
		return nil, fmt.Errorf("compute.Handler: %w", err)
	}
	return adaptor.FiberApp(app.Fiber()), nil
}
