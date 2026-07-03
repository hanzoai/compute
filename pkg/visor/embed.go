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
// (github.com/hanzoai/cloud) can mount visor's /v1 Beego surface in the same
// address space — the same routers, controllers and filters the standalone
// server runs, minus the listener.
//
// There is ONE boot path. Both cmd main() and the embedded cloud mount call
// Bootstrap; the only difference is who owns the listener (main.go calls
// beego.Run; the cloud mount serves Handler() behind its own zip.App).
package visor

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/beego/beego"
	"github.com/beego/beego/plugins/cors"
	"github.com/beego/beego/session"
	_ "github.com/beego/beego/session/redis"

	"github.com/hanzoai/visor/authz"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/routers"
	"github.com/hanzoai/visor/task"
	"github.com/hanzoai/visor/util"
)

// Bootstrap runs visor's full in-process initialization — DB adapter, authz,
// IP/UA parsers, HTTP filters, session config and background tickers — exactly
// as the standalone server does, MINUS binding a listener. It is the single
// boot path shared by cmd main() and the embedded cloud mount, so the two can
// never drift.
func Bootstrap() {
	object.InitAdapter()
	authz.InitAuthz()
	util.InitIpDb()
	util.InitParser()

	installFilters()
	configureSessions()

	task.NewTicker().SetupTicker()
}

// installFilters wires the CORS, static and tenant/api/record filter chain onto
// the global Beego app — identical to the standalone server's registration.
func installFilters() {
	beego.InsertFilter("*", beego.BeforeRouter, cors.Allow(&cors.Options{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "DELETE", "PUT", "PATCH", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "X-Requested-With", "Content-Type", "Accept"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
	}))

	beego.SetStaticPath("/swagger", "swagger")
	beego.InsertFilter("/", beego.BeforeRouter, routers.TransparentStatic) // default page
	beego.InsertFilter("/*", beego.BeforeRouter, routers.TransparentStatic)
	beego.InsertFilter("*", beego.BeforeRouter, routers.TenantContextFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.ApiFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.RecordMessage)
	beego.InsertFilter("*", beego.AfterExec, routers.AfterRecordMessage, false)
}

// configureSessions sets the session provider (file when no redisEndpoint is
// configured, redis otherwise) and GC lifetime, matching the standalone server.
func configureSessions() {
	if beego.AppConfig.String("redisEndpoint") == "" {
		beego.BConfig.WebConfig.Session.SessionProvider = "file"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = "./tmp"
	} else {
		beego.BConfig.WebConfig.Session.SessionProvider = "redis"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = beego.AppConfig.String("redisEndpoint")
	}
	beego.BConfig.WebConfig.Session.SessionGCMaxLifetime = 3600 * 24 * 365
}

// Handler boots visor in-process and returns its Beego HTTP handler for a
// parent binary to mount behind its own listener. It runs Bootstrap, then
// initializes the Beego session manager — normally created inside beego.Run's
// registerSession hook, which the embed path skips — and returns the wired
// handler.
//
// Beego's BeeApp is a process singleton, so Handler must be called at most once
// per process. Any panic from the underlying Beego/DB bootstrap is recovered
// and returned as an error so a parent's mount fails cleanly (fail-closed)
// rather than crashing the whole process.
func Handler() (h http.Handler, err error) {
	defer func() {
		if r := recover(); r != nil {
			h, err = nil, fmt.Errorf("visor.Handler: bootstrap panicked: %v", r)
		}
	}()
	Bootstrap()
	if e := initSession(); e != nil {
		return nil, fmt.Errorf("visor.Handler: session init: %w", e)
	}
	return beego.BeeApp.Handlers, nil
}

// initSession mirrors Beego's unexported registerSession hook (normally fired
// inside beego.Run). The embed path never calls beego.Run, so without this the
// router nil-derefs the moment a controller touches the session store. The
// config mirrors registerSession's own defaulting from BConfig.WebConfig.Session.
// Idempotent: reuses an already-initialized manager.
func initSession() error {
	if beego.GlobalSessions != nil {
		return nil
	}
	s := beego.BConfig.WebConfig.Session
	conf := &session.ManagerConfig{
		CookieName:              s.SessionName,
		EnableSetCookie:         s.SessionAutoSetCookie,
		Gclifetime:              s.SessionGCMaxLifetime,
		Secure:                  beego.BConfig.Listen.EnableHTTPS,
		CookieLifeTime:          s.SessionCookieLifeTime,
		ProviderConfig:          filepath.ToSlash(s.SessionProviderConfig),
		DisableHTTPOnly:         s.SessionDisableHTTPOnly,
		Domain:                  s.SessionDomain,
		EnableSidInHTTPHeader:   s.SessionEnableSidInHTTPHeader,
		SessionNameInHTTPHeader: s.SessionNameInHTTPHeader,
		EnableSidInURLQuery:     s.SessionEnableSidInURLQuery,
		CookieSameSite:          s.SessionCookieSameSite,
	}
	mgr, err := session.NewManager(s.SessionProvider, conf)
	if err != nil {
		return err
	}
	beego.GlobalSessions = mgr
	go mgr.GC()
	return nil
}
