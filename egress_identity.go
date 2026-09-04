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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// identity mints the access token compute presents to egress.
//
// It is compute's OWN IAM identity — the clientId and clientSecret this process
// already signs in with — exchanged for an access token, not a second
// credential minted for this purpose. Egress verifies it the way every other
// service verifies a caller: `iss` against the issuer, `aud` against the
// audience, signature against the published JWKS. So there is one authority,
// one kind of token, and nothing to paste into a config file.
//
// A static bearer would be the alternative and it is worse in the way that
// matters: it does not expire, so it is a credential in a config value that
// nobody rotates, and it says nothing about WHO is calling — which is the one
// question egress exists to answer before it spends money.
type identity struct {
	endpoint string // IAM, e.g. https://hanzo.id
	id       string // clientId
	secret   string // clientSecret
	audience string // the `aud` egress requires (RFC 8707 resource)

	client *http.Client

	mu    sync.Mutex
	held  string
	until time.Time
}

// early is how long before expiry a held token stops being offered. A token that
// expires in flight is a 401 the caller cannot distinguish from a revoked
// identity, so it is replaced while it still works.
const early = 60 * time.Second

// token returns a live access token, minting one when what is held is gone or
// nearly so.
func (i *identity) token() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.held != "" && time.Now().Before(i.until.Add(-early)) {
		return i.held, nil
	}
	tok, ttl, err := i.mint()
	if err != nil {
		return "", err
	}
	i.held, i.until = tok, time.Now().Add(ttl)
	return tok, nil
}

// mint performs the client_credentials exchange (HIP-0111), scoped to egress by
// RFC 8707 `resource` so the token names what it may spend at and is useless
// anywhere else.
func (i *identity) mint() (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if i.audience != "" {
		form.Set("resource", i.audience)
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(i.endpoint, "/")+"/v1/iam/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.SetBasicAuth(i.id, i.secret) // client_secret_basic
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := i.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("egress identity: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, fmt.Errorf("egress identity: %s answered %d with no readable body", i.endpoint, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		// Say which identity was refused. A 401 here reads identically whether
		// the client id is wrong, the secret is stale, or the app may not use
		// this grant, and the reader is holding none of those.
		return "", 0, fmt.Errorf("egress identity: %s refused client %q: %d %s %s",
			i.endpoint, i.id, resp.StatusCode, out.Error, out.Description)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return out.AccessToken, ttl, nil
}
