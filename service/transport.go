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
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// providerTimeout bounds every call visor makes to a cloud. An HTTP client with
// no timeout waits as long as the socket stays open, and a provision waits under
// the org's provisioning hold.
const providerTimeout = 30 * time.Second

// Carrier builds the http.Client a provider SDK makes its calls with.
//
// This is the ONE place a cloud credential turns into an outbound request, which
// is what makes it the one place the credential can stop being here. Registered
// with a carrier that reaches hanzoai/egress, visor sends the request and egress
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
// Unregistered, visor holds the token itself and calls the cloud directly —
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

// ErrTenantNotCarried is a tenant's own cloud account asked of the carrier.
var ErrTenantNotCarried = errors.New("a tenant's own cloud account is not carried through egress")

// ErrHostedNotARow is a provider row naming the hosted compute account.
var ErrHostedNotARow = errors.New("the hosted compute account is reached by hosted compute alone, never by a provider row")

// SuperAdminOrg is the reserved org whose membership is platform SuperAdmin,
// and the one org whose provider rows are the platform's own accounts.
const SuperAdminOrg = "admin"

// IsSuperAdmin is the one SuperAdmin predicate: owner is the reserved admin org.
// An org's own administrator is not SuperAdmin, and no other org's rows are the
// platform's, however that org is named or who is in it.
func IsSuperAdmin(owner string) bool { return owner == SuperAdminOrg }

// httpFor returns the client for one account. It is the only caller of the
// registered carrier, so "how does visor reach a cloud" has one answer.
//
// The carrier spends as THIS service: egress resolves the account label under
// compute's own identity, whose org is the platform's. A tenant's provider row
// names its own label, and carried like that it would reach whatever compute
// may spend under that name — the platform's own account, if the tenant names
// its row after one. So a tenant's own account is never carried: egress has no
// custody that is the tenant's under compute's identity, and borrowing compute's
// is the one thing that must not happen.
//
// And no row reaches the hosted compute account: it is AWS under hostedLabel,
// the one account egress lets compute spend for its hosted machines, and a row
// of that name would drive it through the bring-your-own path — listing every
// tenant's instance into the row's org and stopping them. Hosted compute reaches
// it through carried, and nothing else does.
func httpFor(p Credential) (*http.Client, error) {
	if !carrierRegistered() {
		return directHTTP(), nil
	}
	if p.Tenant != "" {
		return nil, fmt.Errorf("%w: %s's %s account %q", ErrTenantNotCarried, p.Tenant, p.Provider, p.Name)
	}
	if p.Provider == "AWS" && p.Name == hostedLabel {
		return nil, ErrHostedNotARow
	}
	return carried(p)
}

// carried is the registered carrier's client for p, which the caller has already
// decided may be carried: httpFor for a row, hostedEC2 for the hosted account.
func carried(p Credential) (*http.Client, error) {
	carrierMu.RLock()
	c := carrier
	carrierMu.RUnlock()
	if c == nil {
		return nil, errNoEgress
	}
	return c(p)
}

// directHTTP is the carrier-less client: visor's own transport, bounded, with no
// credential attached — the SDK adds that from the token it was handed.
func directHTTP() *http.Client {
	return &http.Client{Timeout: providerTimeout}
}
