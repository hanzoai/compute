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
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/compute/service/commercetest"
	"github.com/hanzoai/compute/service/ec2test"
)

func redDebits(t *testing.T) (func() map[string]int64, *sync.Mutex) {
	var mu sync.Mutex
	got := map[string]int64{}
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage"):
			u := commercetest.Read(r)
			mu.Lock()
			got[u.ID] = u.Cents()
			mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_, _ = w.Write([]byte(`{"available":100000000,"currency":"usd"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	return func() map[string]int64 {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int64{}
		for k, v := range got {
			out[k] = v
		}
		return out
	}, &mu
}

// Round 3 found the first sweep after rollout asking one GetMetricData for 168
// hours of 120 machines and reading it through a 1 MiB limit. The sweep now asks
// only the last settled hour, and a read of any range pages by MaxDatapoints: the
// same 120 x 169 hours read whole, and the sweep charges none of it.
func TestRedTransferBackfillOverOneMiBBillsNothing(t *testing.T) {
	f := hostedFake(t)
	debits, _ := redDebits(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	const n = 120
	var instances []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("m-%020x", 0xabc000+i)
		inst := f.Add(ec2test.Instance{Type: "t3.medium", State: "running", LaunchTime: now.Add(-10 * 24 * time.Hour),
			Tags: map[string]string{orgTagKey: "acme", machineTagKey: id, managedByKey: managedBy}})
		instances = append(instances, inst.ID)
		MarkBilled(id, now)
		for h := 2; h <= 170; h++ {
			f.Send(inst.ID, now.Add(-time.Duration(h)*time.Hour), gibBytes+12345)
		}
	}
	sent, err := readOutbound(context.Background(), instances, now.Add(-170*time.Hour).Truncate(time.Hour), now.Add(-time.Hour).Truncate(time.Hour))
	if err != nil {
		t.Fatalf("%d machines x 169 hours of NetworkOut: %v", n, err)
	}
	for _, id := range instances {
		if len(sent[id]) != 169 {
			t.Fatalf("%s: %d hours read, want 169", id, len(sent[id]))
		}
	}
	MeterRunningMachines(context.Background(), now)
	for id := range debits() {
		if strings.Contains(id, "-transfer-") {
			t.Fatalf("transfer was charged: %s", id)
		}
	}
}

// Round 3 found the transfer of a machine terminated before its hours settled
// never billed. Transfer is charged for no machine now, so there is no such
// hour: the terminated machine's 2250 GiB, and a running machine's, charge
// nothing.
func TestRedTransferOfATerminatedMachineIsNeverBilled(t *testing.T) {
	f := hostedFake(t)
	debits, _ := redDebits(t)
	start := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	inst := f.Add(ec2test.Instance{Type: "c7i.xlarge", State: "running", LaunchTime: start,
		Tags: map[string]string{orgTagKey: "acme", machineTagKey: "m-dddddddddddddddddddd", managedByKey: managedBy}})
	MarkBilled("m-dddddddddddddddddddd", start)
	f.Send(inst.ID, start, 2250*gibBytes)
	f.SetState(inst.ID, "terminated")
	for h := 1; h <= 4; h++ {
		MeterRunningMachines(context.Background(), start.Add(time.Duration(h)*time.Hour))
	}
	for id, c := range debits() {
		if strings.Contains(id, "-transfer-") {
			t.Fatalf("transfer was charged: %s = %d cents", id, c)
		}
	}
}

// The first sweep after rollout bills transfer a machine sent before transfer
// had a price: a machine with no transfer mark is owed from its last start.
func TestRedRolloutBillsTransferFromBeforeThePriceExisted(t *testing.T) {
	f := hostedFake(t)
	debits, _ := redDebits(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	inst := f.Add(ec2test.Instance{Type: "t3.medium", State: "running", LaunchTime: now.Add(-72 * time.Hour),
		Tags: map[string]string{orgTagKey: "acme", machineTagKey: "m-eeeeeeeeeeeeeeeeeeee", managedByKey: managedBy}})
	MarkBilled("m-eeeeeeeeeeeeeeeeeeee", now)
	for h := 2; h <= 72; h++ {
		f.Send(inst.ID, now.Add(-time.Duration(h)*time.Hour), 10*gibBytes)
	}
	MeterRunningMachines(context.Background(), now)
	var cents int64
	n := 0
	for id, c := range debits() {
		if strings.Contains(id, "-transfer-") {
			cents += c
			n++
		}
	}
	if n > 1 {
		t.Fatalf("the first sweep charged %d past hours of transfer, %d cents, for traffic sent before the deploy", n, cents)
	}
}
