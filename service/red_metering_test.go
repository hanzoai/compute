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
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/compute/service/commercetest"
)

// The sweep moves each machine's mark before it debits, so a debit commerce
// refuses — an outage, a 5xx, a timeout — is an hour the ledger says is billed
// and commerce never charged. Every debit carries its hour's MeterID and
// commerce debits one id once, so re-sending an hour is harmless and dropping
// one is revenue gone: the next sweep starts after the mark and never asks for
// it again.
func TestRedAnHourCommerceRefusedIsStillOwed(t *testing.T) {
	freshLedger(t)
	seedCatalog(t, priced("s", 5))
	var (
		mu   sync.Mutex
		down = true
		ids  []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		u := commercetest.Read(r)
		mu.Lock()
		defer mu.Unlock()
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ids = append(ids, u.ID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+u.ID+`","org":"`+u.Org+`","account":"`+u.Org+`"}`)
	})
	commercetest.Serve(t, mux)

	launched := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	LaunchCharge("owed", launched)
	m := []*Machine{{Id: "owed", Size: "s", Tag: "hanzo-org:acme", CreatedTime: launched.Format(time.RFC3339)}}

	meterMachines(context.Background(), m, time.Date(2026, 7, 2, 16, 2, 0, 0, time.UTC)) // commerce down
	mu.Lock()
	down = false
	mu.Unlock()
	meterMachines(context.Background(), m, time.Date(2026, 7, 2, 17, 2, 0, 0, time.UTC)) // commerce back

	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(ids, "compute-owed-2026070216") {
		t.Fatalf("debits after commerce came back = %v: hour 16 ran, was refused once, and is never billed", ids)
	}
}
