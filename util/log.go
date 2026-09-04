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

package util

import (
	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/logs"
)

// LogInfo writes a request-scoped info line prefixed with the caller IP, read
// from the ZAP request context (x-forwarded-for, else the fasthttp remote addr).
func LogInfo(c *zip.Ctx, f string, v ...interface{}) {
	ipString := "(" + ClientIPFromCtx(c) + ") "
	logs.Info(ipString+f, v...)
}

// ClientIPFromCtx resolves the formatted client-IP description from a ZAP
// request context — the ZAP counterpart of GetIPFromRequest.
func ClientIPFromCtx(c *zip.Ctx) string {
	remote := ""
	if fc := c.Fiber(); fc != nil {
		if rc := fc.RequestCtx(); rc != nil {
			remote = rc.RemoteAddr().String()
		}
	}
	return GetClientIP(c.Header("x-forwarded-for"), remote)
}
