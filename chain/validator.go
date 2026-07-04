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

// validator.go reads the hanzo.network (the Hanzo EVM) mainnet validator set — the
// source of truth for the FREE fleet-billing tier. A connected box whose validator
// address is in the set is natively available to the cloud at no fee (it already
// earns validator economics and secures the chain). The chain is READ, never
// written, and is authoritative.
//
// HONESTY CONTRACT (never fabricate an on-chain fact): the free exemption is granted
// ONLY on a POSITIVE, verified membership read. When the lookup is not wired, or a
// wired lookup errors, the exemption is NOT granted — it is flagged "pending chain
// wiring" / "lookup error" and the box is billed like any device until the chain
// confirms it. So a real validator's exemption activates automatically once the
// operator wires the lookup; nothing is ever exempted on an unverified claim.
package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Exemption status strings surfaced to the meter, logs, and the connect response.
const (
	ValidatorStatusExempt  = "exempt"               // verified on-chain validator → free
	ValidatorStatusBilled  = "billed"               // verified NOT a validator → billed as a device
	ValidatorStatusPending = "pending-chain-wiring" // lookup unwired → cannot verify → billed, flagged
	ValidatorStatusError   = "lookup-error"         // wired lookup failed → billed (fail-safe), flagged
)

// validatorIndexerURL is the KMS-provisioned indexer endpoint that returns the
// current hanzo.network validator set as JSON (indexer → read). Empty ⇒ the lookup
// is not wired and the validator exemption is pending.
func validatorIndexerURL() string {
	return strings.TrimSpace(os.Getenv("HANZO_VALIDATOR_INDEXER_URL"))
}

// ValidatorLookupWired reports whether the validator-set lookup is configured.
func ValidatorLookupWired() bool { return validatorIndexerURL() != "" }

// ValidatorExemption reports whether address qualifies for the free validator tier.
//
//	not wired          -> (false, ValidatorStatusPending, nil)  // never fabricate; billed, flagged
//	wired + in set     -> (true,  ValidatorStatusExempt,  nil)  // free / native
//	wired + not in set -> (false, ValidatorStatusBilled,  nil)  // billed as a device
//	wired + error      -> (false, ValidatorStatusError,   err)  // fail-safe: billed, error surfaced
//
// The caller grants the free tier iff the returned bool is true, and records status
// for observability regardless.
func ValidatorExemption(ctx context.Context, address string) (bool, string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return false, ValidatorStatusBilled, nil
	}
	if !ValidatorLookupWired() {
		return false, ValidatorStatusPending, nil
	}
	set, err := fetchValidatorSet(ctx, validatorIndexerURL())
	if err != nil {
		return false, ValidatorStatusError, err
	}
	if set[address] {
		return true, ValidatorStatusExempt, nil
	}
	return false, ValidatorStatusBilled, nil
}

// validatorSetResponse tolerates both an indexer that returns a bare JSON array of
// addresses and one that wraps them under a "validators" key.
type validatorSetResponse struct {
	Validators []string `json:"validators"`
}

// fetchValidatorSet GETs the indexer and returns the validator addresses as a
// lowercase set for O(1) membership. It parses either a bare array (["0x…"]) or an
// object ({"validators":["0x…"]}) — the two shapes indexers commonly emit.
func fetchValidatorSet(ctx context.Context, url string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("validator lookup: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("validator lookup: indexer unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("validator lookup: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("validator lookup: indexer status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var list []string
	if err := json.Unmarshal(body, &list); err != nil {
		var wrapped validatorSetResponse
		if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
			return nil, fmt.Errorf("validator lookup: decode set: %w", err)
		}
		list = wrapped.Validators
	}

	set := make(map[string]bool, len(list))
	for _, a := range list {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			set[a] = true
		}
	}
	return set, nil
}
