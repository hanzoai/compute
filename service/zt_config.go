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

package service

import (
	"strings"

	"github.com/hanzoai/compute/conf"
)

// ZTDialerFromConfig is the deployment's fabric dialer, or nil when it holds no
// IAM identity. There is deliberately NO second credential to configure: the
// clientId/clientSecret visor already signs in with are exchanged for an IAM
// access token at iamEndpoint, and the controller trusts IAM as its signer.
//
// Env/config:
//
//	IAM_CLIENT_ID, IAM_CLIENT_SECRET   the service identity (required)
//	iamEndpoint                        IAM base URL (env wins; conf default)
//	ZT_CONTROLLER_URL                  controller base URL (optional, default
//	                                   https://zt-api.hanzo.ai)
//
// A deployment with no identity gets nil, and the computer handler answers
// 503 — it never falls back to a public dial of a `.zt` name.
func ZTDialerFromConfig() ZTDialer {
	id := strings.TrimSpace(conf.GetConfigString("clientId"))
	secret := strings.TrimSpace(conf.GetConfigString("clientSecret"))
	iam := strings.TrimSpace(conf.GetConfigString("iamEndpoint"))
	if id == "" || secret == "" || iam == "" {
		return nil
	}
	base := strings.TrimSpace(conf.GetConfigString("ztControllerUrl"))
	if base == "" {
		base = DefaultZTController
	}
	return NewLazyZTDialer(base, iamTokenSource{identity: NewIdentity(iam, id, secret, "", nil)})
}
