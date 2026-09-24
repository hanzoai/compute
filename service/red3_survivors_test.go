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
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/compute/service/commercetest"
)

// Kills M3a: Provision records the hour only after the debit lands.
func TestRedAFailedLaunchDebitRecordsNothing(t *testing.T) {
	freshLedger(t)
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage"):
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_, _ = w.Write([]byte(`{"available":100000,"currency":"usd"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	at := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	_ = Provision(context.Background(), "acme", "", 5, 5, "s",
		func() (string, error) { return LaunchCharge("launched", at), nil },
		func() { MarkBilled("launched", at) })
	if m, _ := billed().Through("launched"); m.Hour != "" {
		t.Fatalf("a launch whose debit commerce refused is recorded billed through %s", m.Hour)
	}
}

// Kills M3c and M2e: a past hour whose debit was refused stays owed even when
// the hour after it is paid, and even when the current hour's debit lands.
func TestRedARefusedPastHourIsNeverSkipped(t *testing.T) {
	freshLedger(t)
	seedCatalog(t, priced("s", 5))
	var (
		mu     sync.Mutex
		refuse = map[string]bool{"compute-owed-2026070215": true}
		ids    []string
	)
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage"):
			u := commercetest.Read(r)
			mu.Lock()
			defer mu.Unlock()
			if refuse[u.ID] {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			ids = append(ids, u.ID)
			_, _ = io.WriteString(w, `{}`)
			return
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_, _ = w.Write([]byte(`{"available":100000,"currency":"usd"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	launched := time.Date(2026, 7, 2, 15, 5, 0, 0, time.UTC)
	m := []*Machine{{Id: "owed", Size: "s", Tag: "hanzo-org:acme", CreatedTime: launched.Format(time.RFC3339)}}
	meterMachines(context.Background(), m, time.Date(2026, 7, 2, 16, 2, 0, 0, time.UTC))
	mu.Lock()
	refuse = map[string]bool{}
	mu.Unlock()
	meterMachines(context.Background(), m, time.Date(2026, 7, 2, 17, 2, 0, 0, time.UTC))
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(ids, "compute-owed-2026070215") {
		t.Fatalf("debits = %v: hour 15 ran, its debit was refused once, and it is never billed", ids)
	}
}

// Kills M7a: the GiB is 2^30 bytes, stated independently of gibBytes.
func TestRedTheGBIsTwoToTheThirty(t *testing.T) {
	if c := transferCents(1 << 30); c != 12 {
		t.Fatalf("2^30 bytes = %d cents", c)
	}
	if c := transferCents(1_000_000_000); c != 12 {
		t.Fatalf("10^9 bytes = %d cents, want 12: 11.18 rounded up (a GiB is 2^30 bytes)", c)
	}
	if c := transferCents(894_784_853); c != 10 {
		t.Fatalf("894,784,853 bytes = %d cents, want 10: exactly 9.99999999 rounded up", c)
	}
}
