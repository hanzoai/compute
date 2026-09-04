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

package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/egress/spend"

	"github.com/hanzoai/compute/conf"
	"github.com/hanzoai/compute/service"
)

// carry sends every cloud call through hanzoai/egress, so this process stops
// holding cloud credentials.
//
// Unconfigured it does nothing and compute calls clouds directly with the tokens
// on its Provider rows, which is what it has always done and what a local or
// single-binary run wants. Configured, the tokens are egress's: reading this
// pod's environment, config or memory yields nothing that spends.
//
// What compute still holds is its OWN token, and that is the trade rather than an
// oversight. A stolen caller token buys metered calls through our meter — rate
// limited, attributed, audited, revocable in one place — instead of a vendor
// key that spends without limit, off our network, and takes a rotation across
// every cloud to undo.
func carry() error {
	address := strings.TrimSpace(conf.GetConfigString("egressAddress"))
	if address == "" {
		return nil
	}
	// WHO IS CALLING IS ASKED OF IAM, not of a config value. compute already signs
	// in with a clientId and clientSecret; egress verifies what those buy the
	// same way every service verifies a caller — issuer, audience, JWKS. So
	// there is no second credential to mint, paste or rotate, and the token
	// expires on its own.
	//
	// Refusing to start is the point. An operator who set an address means the
	// cloud keys to be gone from here, and coming up without an identity would
	// serve every cloud call as a 401 whose obvious repair is to put the keys
	// back.
	id := strings.TrimSpace(conf.GetConfigString("clientId"))
	secret := strings.TrimSpace(conf.GetConfigString("clientSecret"))
	iam := strings.TrimSpace(conf.GetConfigString("iamEndpoint"))
	if id == "" || secret == "" || iam == "" {
		return fmt.Errorf("egressAddress is set but compute has no IAM identity "+
			"(clientId=%q iamEndpoint=%q): it cannot say who it is to egress", id, iam)
	}
	who := &identity{
		endpoint: iam, id: id, secret: secret,
		audience: strings.TrimSpace(conf.GetConfigString("egressAudience")),
		client:   &http.Client{Timeout: 30 * time.Second},
	}
	network, address := dial(address)
	service.RegisterCarrier(func(c service.Credential) (*http.Client, error) {
		token, err := who.token()
		if err != nil {
			return nil, err
		}
		return spend.Client(spend.Config{
			Network:  network,
			Address:  address,
			Token:    token,
			Provider: c.Provider,
			Account:  c.Name,
		}), nil
	})
	return nil
}

// dial splits an egress address into the network and the address, so one
// configuration value covers a socket on this host and a service across the
// network. Bare means tcp, which is the common case.
//
//	tcp://egress.hanzo.ai:9653
//	unix:///run/hanzo/egress.sock
//	egress.hanzo.ai:9653
func dial(address string) (string, string) {
	for _, network := range []string{"unix", "tcp"} {
		if rest, ok := strings.CutPrefix(address, network+"://"); ok {
			return network, rest
		}
	}
	return "tcp", address
}
