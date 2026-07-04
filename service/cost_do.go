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

package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// doCostReader reads a BYOC DigitalOcean account's spend via the customer balance
// endpoint (GET /v2/customers/my/balance → month_to_date_usage). The balance API is
// account-wide from the API token, so no extra cost-read scope is needed beyond the
// token already stored to manage machines — the token needs read access to billing,
// which the standard DO API token has.
type doCostReader struct {
	client *godo.Client
}

// newDOCostReader builds the reader from the BYOC token (clientSecret, or clientId
// as the DO convention allows the token in either field). An empty token yields
// ErrCostUnavailable so an unconfigured provider is skipped, never guessed.
func newDOCostReader(clientId, clientSecret string) (CloudCostReader, error) {
	token := strings.TrimSpace(clientSecret)
	if token == "" {
		token = strings.TrimSpace(clientId)
	}
	if token == "" {
		return nil, ErrCostUnavailable
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	client := godo.NewClient(oauth2.NewClient(context.Background(), ts))
	return &doCostReader{client: client}, nil
}

// MonthToDateCents reads the account's month-to-date usage. The balance endpoint is
// always current, so `now` is unused (the DO API has no historical-month selector).
func (r *doCostReader) MonthToDateCents(ctx context.Context, _ time.Time) (int64, error) {
	bal, _, err := r.client.Balance.Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("do cost: get balance: %w", err)
	}
	return dollarStringToCents(bal.MonthToDateUsage), nil
}

// dollarStringToCents parses a DigitalOcean decimal dollar string ("12.34") into
// whole cents, rounding to the nearest cent. Empty/unparseable → 0 (treated as no
// spend, never a fabricated charge).
func dollarStringToCents(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int64(math.Round(f * 100))
}
