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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"
	"github.com/hanzozt/edge-api/rest_util"
	apis "github.com/hanzozt/sdk-golang/edge-apis"
	"github.com/hanzozt/sdk-golang/zt"
	"golang.org/x/oauth2"
)

// DefaultZTController is the controller a deployment that says nothing uses.
// It is a NAME, not a credential: the identity comes from IAM at request time.
const DefaultZTController = "https://zt-api.hanzo.ai"

// ZTDialer is the one seam the computer tunnel needs from the Zero Trust
// fabric: dial a named service and hand back a plain connection. hanzod is
// published as `hanzod.<org>.zt` by `hanzo link host` — host mode cannot
// intercept DNS, so a consumer dials the name through a fabric client instead
// of resolving it. Nothing here publishes a port or touches DNS.
type ZTDialer interface {
	Dial(service string) (net.Conn, error)
}

var (
	ztMu     sync.RWMutex
	ztDialer ZTDialer
)

// RegisterZTDialer installs the process-wide fabric dialer. nil unregisters,
// which is how a deployment without an IAM identity fails CLOSED: the computer
// handler answers 503 rather than dialling a public address that happens to
// share the service's name.
func RegisterZTDialer(d ZTDialer) {
	ztMu.Lock()
	ztDialer = d
	ztMu.Unlock()
}

// CurrentZTDialer returns the installed dialer, or nil when this deployment
// has no fabric identity. Callers must fail closed on nil.
func CurrentZTDialer() ZTDialer {
	ztMu.RLock()
	defer ztMu.RUnlock()
	return ztDialer
}

// NewLazyZTDialer builds a dialer that opens its fabric context on first use.
// Opening at boot would reach the controller and read its CAs before this
// process is serving; a controller that is briefly down then takes the whole
// service with it. A failed open is NOT cached, so the next dial tries again.
func NewLazyZTDialer(base string, src oauth2.TokenSource) ZTDialer {
	return &lazyZT{base: strings.TrimRight(strings.TrimSpace(base), "/"), src: src}
}

type lazyZT struct {
	base string
	src  oauth2.TokenSource

	mu  sync.Mutex
	ctx zt.Context
}

func (d *lazyZT) Dial(service string) (net.Conn, error) {
	ctx, err := d.context()
	if err != nil {
		return nil, err
	}
	return ctx.Dial(service)
}

func (d *lazyZT) context() (zt.Context, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		return d.ctx, nil
	}
	ctx, err := openZT(d.base, d.src)
	if err != nil {
		return nil, err
	}
	d.ctx = ctx
	return ctx, nil
}

// openZT signs in to the controller by ext-jwt with a token read from src at
// every login. The system pool verifies the controller; the CAs it publishes
// at /.well-known/est/cacerts are added to verify the edge routers a dial
// crosses. Trust is two roots, never "insecure skip verify".
func openZT(base string, src oauth2.TokenSource) (zt.Context, error) {
	if base == "" {
		return nil, fmt.Errorf("ZT controller is not configured")
	}
	if src == nil {
		return nil, fmt.Errorf("dialing a .zt service requires an IAM identity")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("read the system roots: %w", err)
	}
	cas, err := rest_util.GetControllerWellKnownCasWithTlsConfig(base, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, fmt.Errorf("read the fabric CAs from %s: %w", base, err)
	}
	pool := roots.Clone()
	for _, ca := range cas {
		pool.AddCert(ca)
	}
	cred := &ztBearer{src: src}
	cred.CaPool = pool
	ctx, err := zt.NewContext(&zt.Config{ZtAPI: base + "/edge/client/v1", Credentials: cred})
	if err != nil {
		return nil, fmt.Errorf("open the fabric context: %w", err)
	}
	ctrl := ctx.(*zt.ContextImpl).CtrlClt
	// POST /authenticate?method=ext-jwt is the one way in, so the SDK never
	// takes the OIDC flow it would pick when the controller offers one.
	ctrl.SetUseOidc(false)
	// Building the context started a background read of the controller's
	// version; every login rewrites the TLS config that read dials with.
	// Waiting here orders the two.
	ctrl.API.ControllerSupportsOidc()
	return ctx, nil
}

// ztBearer is the ext-jwt credential whose token comes from IAM on every
// login, never fixed when the context opened — a long-lived tunnel must not
// present a stale token after the first hour.
type ztBearer struct {
	apis.JwtCredentials
	src oauth2.TokenSource
}

func (b *ztBearer) AuthenticateRequest(r runtime.ClientRequest, reg strfmt.Registry) error {
	t, err := b.src.Token()
	if err != nil {
		return fmt.Errorf("mint the IAM token: %w", err)
	}
	if err := b.BaseCredentials.AuthenticateRequest(r, reg); err != nil {
		return err
	}
	return r.SetHeaderParam("Authorization", "Bearer "+t.AccessToken)
}

// iamTokenSource adapts visor's ONE IAM identity to the oauth2.TokenSource the
// fabric SDK takes. It holds no second credential: the clientId/clientSecret
// are the same ones egress uses, with no audience so the controller's ext-jwt
// verifier sees the plain machine token it expects.
type iamTokenSource struct{ identity *Identity }

func (s iamTokenSource) Token() (*oauth2.Token, error) {
	t, err := s.identity.Token()
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: t, TokenType: "Bearer"}, nil
}
