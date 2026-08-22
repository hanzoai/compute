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

package controllers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"

	"github.com/hanzoai/visor/conf"
	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
	"github.com/hanzoai/visor/util/guacamole"
)

const (
	TunnelClosed       int = -1
	Normal             int = 0
	SessionNotFound    int = 800
	NewTunnelError     int = 801
	ForcedDisconnect   int = 802
	AssetNotActive     int = 803
	ParametersError    int = 804
	AssetNotFound      int = 805
	SessionUpdateError int = 806
)

var UpGrader = websocket.FastHTTPUpgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
		return true
	},
	Subprotocols: []string{"guacamole"},
}

// OpenSession opens a remote session ON an asset — POST /v1/assets/:owner/:name/sessions.
//
// It was POST /v1/add-asset-tunnel?assetId=, which named neither what it made
// nor what it addressed: it makes no tunnel, it makes a SESSION, and the asset
// it makes one on is the parent of that session. The tunnel is what CARRIES the
// session once it exists, and that is a sub-resource of the session (see
// Tunnel).
//
// It stays UNTYPED. The value it mints is an object.Session, whose own
// collection is still the enveloped /v1/*-session surface, and one value
// answered in two shapes is worse than either. It converts with that noun.
func (c *ApiController) OpenSession() {
	assetId := util.GetIdFromOwnerAndName(c.Ctx.Param("owner"), c.Ctx.Param("name"))
	mode := c.Ctx.Query("mode")

	user := c.GetSessionUser()
	if user == nil {
		c.ResponseError("please sign in first")
		return
	}

	session := &object.Session{
		Creator:       user.Name,
		ClientIp:      c.getClientIp(),
		UserAgent:     c.getUserAgent(),
		ClientIpDesc:  util.GetDescFromIP(c.getClientIp()),
		UserAgentDesc: util.GetDescFromUserAgent(c.getUserAgent()),
	}

	var err error
	session, err = object.CreateSession(session, assetId, mode)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(session)
}

// Tunnel carries a session's remote-desktop stream — GET /v1/sessions/:owner/:name/tunnel,
// a websocket speaking the guacamole protocol.
//
// It was GET /v1/get-asset-tunnel?sessionId=, which addressed no asset: it read
// a SESSION id and has always been the stream of that session. The session is
// the parent; the tunnel is the sub-resource. Its display geometry
// (width/height/dpi) and the remote credentials an asset does not itself store
// stay in the query, because they configure this one connection rather than
// name a resource.
//
// It stays UNTYPED, and structurally must: it hijacks the connection, and a
// typed handler is given a context.Context and no request to hijack.
func (c *ApiController) Tunnel() {
	// Read every request-scoped value BEFORE hijacking: the fiber ctx is pooled
	// and recycled once this handler returns, so the post-upgrade callback (which
	// runs on the hijacked connection, after the response) must close over plain
	// values and never touch c.Ctx.
	width := c.Ctx.Query("width")
	height := c.Ctx.Query("height")
	dpi := c.Ctx.Query("dpi")
	sessionId := util.GetIdFromOwnerAndName(c.Ctx.Param("owner"), c.Ctx.Param("name"))
	username := c.Ctx.Query("username")
	password := c.Ctx.Query("password")

	err := UpGrader.Upgrade(c.Ctx.Fiber().RequestCtx(), func(ws *websocket.Conn) {
		// The callback runs in fasthttp's post-response goroutine, outside zip's
		// recover middleware — isolate panics so a tunnel fault never crashes the
		// process (the per-request recovery Beego gave for free).
		defer func() {
			if r := recover(); r != nil {
				logs.Error(fmt.Sprintf("Tunnel(): panic recovered: %v", r))
			}
		}()

		intWidth, err := strconv.Atoi(width)
		if err != nil {
			guacamole.Disconnect(ws, ParametersError, err.Error())
			return
		}
		intHeight, err := strconv.Atoi(height)
		if err != nil {
			guacamole.Disconnect(ws, ParametersError, err.Error())
			return
		}

		session, err := object.GetConnSession(sessionId)
		if err != nil {
			guacamole.Disconnect(ws, SessionNotFound, err.Error())
			return
		}

		machine, err := object.GetMachine(session.Asset)
		if err != nil || machine == nil {
			guacamole.Disconnect(ws, AssetNotFound, err.Error())
			return
		}

		if machine.RemoteUsername == "" {
			machine.RemoteUsername = username
			machine.RemotePassword = password
		} else {
			if machine.RemotePassword == "" {
				machine.RemotePassword = password
			}
		}

		configuration := guacamole.NewConfiguration()
		propertyMap := configuration.LoadConfig()

		setConfig(propertyMap, machine, configuration)
		configuration.SetParameter("width", width)
		configuration.SetParameter("height", height)
		configuration.SetParameter("dpi", dpi)

		addr := conf.GetConfigString("guacamoleEndpoint")
		tunnel, err := guacamole.NewTunnel(addr, configuration)
		if err != nil {
			guacamole.Disconnect(ws, NewTunnelError, err.Error())
			return
		}

		guacSession := &guacamole.Session{
			Id:          sessionId,
			Protocol:    session.Protocol,
			WebSocket:   ws,
			GuacdTunnel: tunnel,
		}

		guacSession.Observer = guacamole.NewObserver(sessionId)
		guacamole.GlobalSessionManager.Add(guacSession)

		session.ConnectionId = tunnel.ConnectionID
		session.Width = intWidth
		session.Height = intHeight
		session.Status = object.Connecting
		session.Recording = configuration.GetParameter(guacamole.RecordingPath)

		if session.Recording == "" {
			// No audit is required when no screen is recorded
			session.Reviewed = true
		}

		_, err = object.UpdateSession(sessionId, session)
		if err != nil {
			guacamole.Disconnect(ws, SessionUpdateError, err.Error())
			return
		}

		guacamoleHandler := NewGuacamoleHandler(ws, tunnel)
		guacamoleHandler.Start()
		defer guacamoleHandler.Stop()

		for {
			_, message, err := ws.ReadMessage()
			if err != nil {
				logs.Error(fmt.Sprintf("Tunnel():ws.ReadMessage() error: %s", err.Error()))

				_ = tunnel.Close()
				err2 := object.CloseSession(sessionId, Normal, "Normal user exit")
				if err2 != nil {
					logs.Error(fmt.Sprintf("Tunnel():object.CloseSession() error: %s", err.Error()))
				}
				return
			}

			_, err = tunnel.WriteAndFlush(message)
			if err != nil {
				logs.Error(fmt.Sprintf("Tunnel():tunnel.WriteAndFlush() error: %s", err.Error()))

				err2 := object.CloseSession(sessionId, Normal, "Normal user exit")
				if err2 != nil {
					logs.Error(fmt.Sprintf("Tunnel():object.CloseSession() (2nd) error: %s", err.Error()))
				}
				return
			}
		}
	})
	if err != nil {
		logs.Error(fmt.Sprintf("Tunnel(): websocket upgrade failed: %s", err.Error()))
	}
}

func (c *ApiController) TunnelMonitor() {
	sessionId := c.Ctx.Query("sessionId")

	err := UpGrader.Upgrade(c.Ctx.Fiber().RequestCtx(), func(ws *websocket.Conn) {
		defer func() {
			if r := recover(); r != nil {
				logs.Error(fmt.Sprintf("TunnelMonitor(): panic recovered: %v", r))
			}
		}()

		s, err := object.GetConnSession(sessionId)
		if err != nil {
			guacamole.Disconnect(ws, SessionNotFound, err.Error())
			return
		}

		if s.Status != object.Connected {
			guacamole.Disconnect(ws, AssetNotActive, "Session offline")
			return
		}

		connectionId := s.ConnectionId
		configuration := guacamole.NewConfiguration()
		configuration.ConnectionID = connectionId
		configuration.SetParameter("width", strconv.Itoa(s.Width))
		configuration.SetParameter("height", strconv.Itoa(s.Height))
		configuration.SetParameter("dpi", "96")
		configuration.SetReadOnlyMode()

		addr := conf.GetConfigString("guacamoleEndpoint")
		tunnel, err := guacamole.NewTunnel(addr, configuration)
		if err != nil {
			guacamole.Disconnect(ws, NewTunnelError, err.Error())
			return
		}

		guacSession := &guacamole.Session{
			Id:          sessionId,
			Protocol:    s.Protocol,
			WebSocket:   ws,
			GuacdTunnel: tunnel,
		}

		forObsSession := guacamole.GlobalSessionManager.Get(sessionId)
		if forObsSession == nil {
			guacamole.Disconnect(ws, SessionNotFound, "Failed to obtain session")
			return
		}
		guacSession.Id = uuid.NewString()
		forObsSession.Observer.Add(guacSession)

		guacamoleHandler := NewGuacamoleHandler(ws, tunnel)
		guacamoleHandler.Start()
		defer guacamoleHandler.Stop()

		for {
			_, message, err := ws.ReadMessage()
			if err != nil {
				_ = tunnel.Close()

				observerId := guacSession.Id
				forObsSession.Observer.Delete(observerId)
				return
			}

			_, err = tunnel.WriteAndFlush(message)
			if err != nil {
				if err := object.CloseSession(sessionId, Normal, "Normal user exit"); err != nil {
					logs.Error(fmt.Sprintf("TunnelMonitor():object.CloseSession() error: %s", err.Error()))
				}
				return
			}
		}
	})
	if err != nil {
		logs.Error(fmt.Sprintf("TunnelMonitor(): websocket upgrade failed: %s", err.Error()))
	}
}

func setConfig(propertyMap map[string]string, machine *object.Machine, configuration *guacamole.Configuration) {
	configuration.Protocol = strings.ToLower(machine.RemoteProtocol)

	hostname := machine.PublicIp
	if hostname == "" {
		hostname = machine.PrivateIp
	}
	if hostname == "" {
		hostname = machine.Name
	}

	configuration.SetParameter("hostname", hostname)
	configuration.SetParameter("port", strconv.Itoa(machine.RemotePort))
	configuration.SetParameter("username", machine.RemoteUsername)
	configuration.SetParameter("password", machine.RemotePassword)

	switch machine.RemoteProtocol {
	case "RDP":
		configuration.SetParameter("security", "any")
		configuration.SetParameter("ignore-cert", "true")
		configuration.SetParameter("create-drive-path", "true")
		configuration.SetParameter("resize-method", "display-update")
		configuration.SetParameter(guacamole.EnableWallpaper, propertyMap[guacamole.EnableWallpaper])
		configuration.SetParameter(guacamole.EnableTheming, propertyMap[guacamole.EnableTheming])
		configuration.SetParameter(guacamole.EnableFontSmoothing, propertyMap[guacamole.EnableFontSmoothing])
		configuration.SetParameter(guacamole.EnableFullWindowDrag, propertyMap[guacamole.EnableFullWindowDrag])
		configuration.SetParameter(guacamole.EnableDesktopComposition, propertyMap[guacamole.EnableDesktopComposition])
		configuration.SetParameter(guacamole.EnableMenuAnimations, propertyMap[guacamole.EnableMenuAnimations])
		configuration.SetParameter(guacamole.DisableBitmapCaching, propertyMap[guacamole.DisableBitmapCaching])
		configuration.SetParameter(guacamole.DisableOffscreenCaching, propertyMap[guacamole.DisableOffscreenCaching])
		configuration.SetParameter(guacamole.ColorDepth, propertyMap[guacamole.ColorDepth])
		configuration.SetParameter(guacamole.ForceLossless, propertyMap[guacamole.ForceLossless])
		configuration.SetParameter(guacamole.PreConnectionId, propertyMap[guacamole.PreConnectionId])
		configuration.SetParameter(guacamole.PreConnectionBlob, propertyMap[guacamole.PreConnectionBlob])

		// if asset.EnableRemoteApp {
		//	remoteApp := asset.RemoteApps[0]
		//	configuration.SetParameter("remote-app", "||"+remoteApp.RemoteAppName)
		//	configuration.SetParameter("remote-app-dir", remoteApp.RemoteAppDir)
		//	configuration.SetParameter("remote-app-args", remoteApp.RemoteAppArgs)
		// }
	case "SSH":
		configuration.SetParameter(guacamole.FontSize, propertyMap[guacamole.FontSize])
		// configuration.SetParameter(guacamole.FontName, propertyMap[guacamole.FontName])
		configuration.SetParameter(guacamole.ColorScheme, propertyMap[guacamole.ColorScheme])
		configuration.SetParameter(guacamole.Backspace, propertyMap[guacamole.Backspace])
		configuration.SetParameter(guacamole.TerminalType, propertyMap[guacamole.TerminalType])
	case "Telnet":
		configuration.SetParameter(guacamole.FontSize, propertyMap[guacamole.FontSize])
		// configuration.SetParameter(guacamole.FontName, propertyMap[guacamole.FontName])
		configuration.SetParameter(guacamole.ColorScheme, propertyMap[guacamole.ColorScheme])
		configuration.SetParameter(guacamole.Backspace, propertyMap[guacamole.Backspace])
		configuration.SetParameter(guacamole.TerminalType, propertyMap[guacamole.TerminalType])
	default:
	}
}
