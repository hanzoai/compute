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

package service

import (
	"net/http"
	"sync"
	"time"
)

// providerTimeout bounds every call compute makes to a cloud. An HTTP client with
// no timeout waits as long as the socket stays open, and a provision waits under
// the org's provisioning hold.
const providerTimeout = 30 * time.Second

// Carrier builds the http.Client a provider SDK makes its calls with.
//
// This is the ONE place a cloud credential turns into an outbound request, which
// is what makes it the one place the credential can stop being here. Registered
// with a carrier that reaches hanzoai/egress, compute sends the request and egress
// attaches the key: the token is never in this process, never in its environment
// and never in its config, so reading a pod here yields nothing to spend.
//
// Both SDKs take an http.Client — godo.NewClient(c), hcloud.WithHTTPClient(c) —
// so this is a transport swap and not an SDK rewrite.
type Carrier func(p Credential) (*http.Client, error)

var (
	carrierMu sync.RWMutex
	carrier   Carrier
)

// RegisterCarrier installs the carrier every provider client is built with.
// Unregistered, compute holds the token itself and calls the cloud directly —
// which is what it did before egress existed and what a single-binary or local
// run still wants.
func RegisterCarrier(c Carrier) {
	carrierMu.Lock()
	defer carrierMu.Unlock()
	carrier = c
}

// carrierRegistered reports whether outbound calls are carried. A provider that
// cannot use the carrier asks this before falling back to holding a token.
func carrierRegistered() bool {
	carrierMu.RLock()
	defer carrierMu.RUnlock()
	return carrier != nil
}

// httpFor returns the client for one account. It is the only caller of the
// registered carrier, so "how does compute reach a cloud" has one answer.
func httpFor(p Credential) (*http.Client, error) {
	carrierMu.RLock()
	c := carrier
	carrierMu.RUnlock()
	if c != nil {
		return c(p)
	}
	return directHTTP(), nil
}

// directHTTP is the carrier-less client: compute's own transport, bounded, with no
// credential attached — the SDK adds that from the token it was handed.
func directHTTP() *http.Client {
	return &http.Client{Timeout: providerTimeout}
}
