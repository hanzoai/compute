// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
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

// commerce.go is visor's ONE client of the commerce API cloud serves: the
// prepaid balance an org holds, its spend-cap verdict, and the usage debit.
//
//	GET  {COMMERCE_URL}/v1/billing/balance            X-Org-Id: <org>
//	GET  {COMMERCE_URL}/v1/billing/alerts/authorize   X-Org-Id: <org>
//	POST {COMMERCE_URL}/v1/billing/usage              X-Org-Id: <org>
//
// Every call carries visor's OWN IAM identity — the clientId and clientSecret
// it signs in with, exchanged for a client_credentials token (Identity) — and
// names the org it is about. The token acts for visor's own org and for every
// org that granted visor membership; for any other org cloud answers for visor's
// own org instead, so the balance read checks the account it was answered for
// and the debit names its org, and cloud refuses a debit for an org the token
// does not act for. Nothing here holds a shared secret.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/compute/conf"
	"github.com/hanzoai/compute/logs"
)

// defaultCommerceURL is cloud in-cluster: the commerce API is served there, at
// the same address every cluster names cloud by.
const defaultCommerceURL = "http://cloud.hanzo.svc:8000"

// commerceTimeout bounds each call. A slow ledger must surface as an error the
// gate refuses on, never hold a provision open.
const commerceTimeout = 5 * time.Second

// commerceHTTP is the bounded client every commerce call rides.
var commerceHTTP = &http.Client{Timeout: commerceTimeout}

// ErrInsufficientBalance is the org's prepaid balance not covering the charge.
var ErrInsufficientBalance = errors.New("insufficient balance")

// ErrSpendCapExceeded is the org's own spend cap refusing the charge.
var ErrSpendCapExceeded = errors.New("spend cap exceeded")

// Charge is one act billed to an org.
type Charge struct {
	// ID names the act; the ledger debits one id once, so a retry is harmless.
	ID      string
	Org     string
	Project string
	Cents   int64
	// Service is the product the charge is for, a lowercase slug.
	Service string
	// Model is the unit the amount prices: a size slug, a tier and label.
	Model string
}

// commerceBase is the commerce API base, COMMERCE_URL or cloud in-cluster.
func commerceBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("COMMERCE_URL")), "/"); v != "" {
		return v
	}
	return defaultCommerceURL
}

// identity is visor's IAM identity, held across calls so a live token is
// reused. It is rebuilt only when the credential it was built from changes.
var (
	identityMu sync.Mutex
	identity   *Identity
)

func billingIdentity() (*Identity, error) {
	endpoint := strings.TrimSpace(conf.GetConfigString("iamEndpoint"))
	id := strings.TrimSpace(conf.GetConfigString("clientId"))
	secret := strings.TrimSpace(conf.GetConfigString("clientSecret"))
	if endpoint == "" || id == "" || secret == "" {
		return nil, errors.New("visor has no IAM identity (clientId, clientSecret, iamEndpoint) to present to commerce")
	}
	identityMu.Lock()
	defer identityMu.Unlock()
	if identity == nil || identity.endpoint != endpoint || identity.id != id || identity.secret != secret {
		identity = NewIdentity(endpoint, id, secret, "", nil)
	}
	return identity, nil
}

// MeteringConfigured reports whether a debit can be presented at all: visor
// holds an IAM identity. The commerce base always resolves (it defaults to cloud
// in-cluster), so the identity is the whole question.
func MeteringConfigured() bool {
	_, err := billingIdentity()
	return err == nil
}

// call issues one commerce request for org and decodes a 2xx answer into out.
func call(ctx context.Context, method, path, org string, body, out any) error {
	who, err := billingIdentity()
	if err != nil {
		return err
	}
	token, err := who.Token()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, commerceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, commerceBase()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := commerceHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("commerce: read %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &commerceRefusal{method: method, path: path, status: resp.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("commerce %s: decode: %w", path, err)
	}
	return nil
}

// commerceRefusal is a commerce answer outside 2xx.
type commerceRefusal struct {
	method, path string
	status       int
	body         string
}

func (e *commerceRefusal) Error() string {
	return fmt.Sprintf("commerce %s %s: %d %s", e.method, e.path, e.status, e.body)
}

// refusesOrg reports a 402: commerce refusing org's balance for the org's own
// sake. An unfunded org is 200 with nothing available; other 4xx are ours.
func refusesOrg(err error) bool {
	var r *commerceRefusal
	return errors.As(err, &r) && r.status == http.StatusPaymentRequired
}

// available is org's spendable prepaid balance in cents.
//
// The answer names the account it was read from, and it must be org's: a token
// that does not act for org is answered for visor's own org instead, and gating
// a tenant's launch on visor's own balance would admit work nobody can bill. An
// answer naming no account cannot be checked and is taken as it stands.
func available(ctx context.Context, org string) (int64, error) {
	var bal struct {
		Available int64  `json:"available"`
		Account   string `json:"account"`
	}
	// Commerce reads a balance by ?user=; the org's pool is the org.
	q := url.Values{"currency": {"usd"}, "user": {org}}
	if err := call(ctx, http.MethodGet, "/v1/billing/balance?"+q.Encode(), org, nil, &bal); err != nil {
		return 0, err
	}
	if bal.Account != "" && bal.Account != org && !strings.HasPrefix(bal.Account, org+"/") {
		return 0, fmt.Errorf("commerce answered the balance of %q, not %q: visor does not act for this org", bal.Account, org)
	}
	return bal.Available, nil
}

// capAllows asks whether org's own spend caps admit cents on project/service.
// A cap is a policy overlay over a funds check that already passed, so an error
// here admits: a limits outage must not stop every launch.
func capAllows(ctx context.Context, org, project, service string, cents int64) bool {
	q := url.Values{"service": {service}, "amount": {strconv.FormatInt(cents, 10)}}
	if project != "" {
		q.Set("project", project)
	}
	var v struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := call(ctx, http.MethodGet, "/v1/billing/alerts/authorize?"+q.Encode(), org, nil, &v); err != nil {
		logs.Warning("commerce: spend-cap verdict for %s unavailable, admitting on funds alone: %v", org, err)
		return true
	}
	return v.Allow || v.Reason != "spend_cap"
}

// Authorize is the funds-then-cap gate for a charge of cents to org. It fails
// closed on any funds answer that is not a clear yes.
func Authorize(ctx context.Context, org, project, service string, cents int64) error {
	have, err := available(ctx, org)
	if err != nil {
		return err
	}
	if have < cents {
		return ErrInsufficientBalance
	}
	if !capAllows(ctx, org, project, service, cents) {
		return ErrSpendCapExceeded
	}
	return nil
}

// Record debits c to its org. It does not gate: the work already happened.
func Record(ctx context.Context, c Charge) error {
	if c.Cents <= 0 {
		return nil
	}
	body := map[string]any{
		"id":     c.ID,
		"org":    c.Org,
		"amount": map[string]string{"decimal": decimal(c.Cents), "currency": "usd"},
	}
	if c.Service != "" {
		body["service"] = c.Service
	}
	if c.Model != "" {
		body["model"] = c.Model
	}
	if c.Project != "" {
		body["project"] = c.Project
	}
	return call(ctx, http.MethodPost, "/v1/billing/usage", c.Org, body, nil)
}

// decimal renders whole cents as an exact USD decimal.
func decimal(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
