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

// ticker_test.go holds the hourly money clock to its two invariants, and it holds
// them against the REAL lease — object.ClaimMeterHour, the real insert-once PK on
// the real coordination store — not a stand-in for it. A fake claim would let both
// tests pass while the thing they are about is broken, which is the exact failure
// mode these tests exist to catch.
package task

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ha"
	"github.com/hanzoai/visor/object"
)

// TestMain gives this package a real store rooted in a temp dir, so ClaimMeterHour
// writes real rows to a real `_global` coordination engine and the primary key that
// makes the lease single-flight is actually exercised.
//
// dataRoot is set as the CONFIG KEY before InitAdapter for the reason the billing
// suite documents: conf resolves a key from the environment first, and the value is
// cached on first read, so setting it late sends the whole suite to the machine's
// real /data.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "visor-ticker-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("dataRoot", root); err != nil {
		panic(err)
	}
	object.InitAdapter()
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// soleWriter makes this process the elected billing owner for the duration of a
// test, which is what lets the real claim path run: claimLease refuses outright
// unless the caller wins the HRW election, so without a membership source every
// claim would return false and both tests would pass for the wrong reason.
//
// ha.Static is the single-process source the production comment names — the sole
// process is the sole writer, so it correctly elects itself. This is the estate's
// existing election, reused; the fix introduces no second mechanism.
func soleWriter(t *testing.T) {
	t.Helper()
	object.RegisterMembership(ha.Static("visor-ticker-test"))
	t.Cleanup(func() { object.RegisterMembership(nil) })
}

// hourSeq hands out hours nothing in this process has used yet.
var hourSeq atomic.Int64

// billedHour returns a fresh hour on every call, so no two claims in this process
// ever collide on the lease primary key.
//
// It counts rather than taking a fixed offset because the lease is a REAL row in a
// REAL store, and that row outlives the test that wrote it — including across
// passes under `go test -count=N`. A fixed hour is unclaimed on the first pass and
// already claimed on the second, so the suite goes red on pass two for a reason
// that has nothing to do with the property under test. Since `-count` is exactly
// how a flaky money test gets caught, the suite has to survive it.
//
// A test that needs the SAME hour twice binds one call to a variable and reuses it;
// that is the point of the first test, and it is a different question from two
// tests wanting different hours.
func billedHour() time.Time {
	return time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC).Add(time.Duration(hourSeq.Add(1)) * time.Hour)
}

// down is the unreachable provider: what a revoked token produces. A revoked token
// is still a non-empty string, so the presence checks all still say "configured"
// and the failure surfaces here, at the call, exactly as it does in production.
var down = errors.New("configured cloud account unreachable: GET https://api.digitalocean.com/v2/account: 401 Unable to authenticate you")

// reachable/unreachable are the two probe outcomes.
func reachable(context.Context) error   { return nil }
func unreachable(context.Context) error { return down }

// TestAnUnreachableProviderDoesNotSpendTheHour is the money invariant this whole
// change exists for.
//
// The lease is insert-once per wall-clock hour with no retry and no reconciliation,
// so claiming it is not "this hour was billed" but "this hour is spent forever". If
// the hour is spent while the provider is unreachable, both sweeps bill zero,
// report success, and that hour's revenue is destroyed rather than delayed — a new
// credential cannot recover it, because the hour is already claimed.
//
// The assertion is deliberately NOT "claim was not called". That is a spy on the
// implementation, and it would pass against a lease that was called and consumed
// anyway. The assertion is the CONSEQUENCE that matters: after the failed hour, the
// SAME hour is still billable. Only an unspent lease can do that.
func TestAnUnreachableProviderDoesNotSpendTheHour(t *testing.T) {
	soleWriter(t)
	now := billedHour()

	var billed atomic.Int32
	h := hour{
		owner:     func() bool { return true },
		reachable: unreachable,
		claim:     object.ClaimMeterHour, // the REAL lease
		meter:     func(context.Context, time.Time) { billed.Add(1) },
	}

	h.run(context.Background(), now)
	if got := billed.Load(); got != 0 {
		t.Fatalf("swept %d times with the provider down, want 0 — billing from an unreachable provider invoices zeros as if they were real", got)
	}

	// The credential returns. The SAME hour must still be billable: an hour we
	// could not bill is a MISSED hour, not a lost one.
	h.reachable = reachable
	h.run(context.Background(), now)
	if got := billed.Load(); got != 1 {
		t.Fatalf("swept %d times for hour %s after the provider came back, want 1 — "+
			"the failed hour spent the lease, so the hour is destroyed and no credential can recover it",
			got, now.UTC().Format("2006010215"))
	}
}

// TestAnHourCannotBeBilledTwice is the invariant the lease was built for, and the
// one the fix above must not have weakened. Double-billing a customer is far worse
// than under-billing, so this is the constraint the ordering change is checked
// against.
//
// It covers both ways an hour gets billed twice: concurrently (two replicas racing
// the same hour, which is why the lease exists at all) and sequentially (a retry, a
// mid-hour restart, or a post-flip new owner re-running a claimed hour).
func TestAnHourCannotBeBilledTwice(t *testing.T) {
	soleWriter(t)
	now := billedHour()

	var billed atomic.Int32
	newHour := func() hour {
		return hour{
			owner:     func() bool { return true },
			reachable: reachable,
			claim:     object.ClaimMeterHour, // the REAL lease
			meter: func(context.Context, time.Time) {
				billed.Add(1)
				time.Sleep(time.Millisecond) // widen any window between claim and debit
			},
		}
	}

	// Concurrent: every replica proves the provider reachable, then races the claim.
	const replicas = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			newHour().run(context.Background(), now)
		}()
	}
	close(start)
	wg.Wait()

	if got := billed.Load(); got != 1 {
		t.Fatalf("%d replicas billed hour %s %d times, want exactly 1 — every extra is a duplicate debit against a real customer",
			replicas, now.UTC().Format("2006010215"), got)
	}

	// Sequential: a retry of an hour already billed must not bill it again.
	newHour().run(context.Background(), now)
	if got := billed.Load(); got != 1 {
		t.Fatalf("a retry billed hour %s again (%d total), want 1 — a restart or an owner flip must not re-bill a claimed hour",
			now.UTC().Format("2006010215"), got)
	}
}

// TestAnUnclaimedHourIsStillOneHour proves the recovery in the first test is the
// LEASE being unspent rather than the sweep being re-runnable: once the hour IS
// billed, it stays billed exactly once even though the earlier attempt failed. It
// is what stops "leave it unclaimed on failure" from turning into "bill it once per
// failure".
func TestAnUnclaimedHourIsStillOneHour(t *testing.T) {
	soleWriter(t)
	now := billedHour()

	var billed atomic.Int32
	h := hour{
		owner:     func() bool { return true },
		reachable: unreachable,
		claim:     object.ClaimMeterHour,
		meter:     func(context.Context, time.Time) { billed.Add(1) },
	}

	// Three failed hours spend nothing.
	for i := 0; i < 3; i++ {
		h.run(context.Background(), now)
	}
	if got := billed.Load(); got != 0 {
		t.Fatalf("billed %d times while the provider was down, want 0", got)
	}

	// The credential returns and stays. The hour is billed once, not four times.
	h.reachable = reachable
	for i := 0; i < 3; i++ {
		h.run(context.Background(), now)
	}
	if got := billed.Load(); got != 1 {
		t.Fatalf("hour %s billed %d times after recovery, want exactly 1", now.UTC().Format("2006010215"), got)
	}
}

// TestANonOwnerNeverProbesOrClaims keeps the election first: a replica that is not
// the elected writer must not even ask the provider. The probe is cheap but it is
// not free, and every replica making it every hour is an N-times amplification of a
// call the estate bounds deliberately.
func TestANonOwnerNeverProbesOrClaims(t *testing.T) {
	soleWriter(t)
	now := billedHour()

	var probed, billed atomic.Int32
	h := hour{
		owner:     func() bool { return false },
		reachable: func(context.Context) error { probed.Add(1); return nil },
		claim:     object.ClaimMeterHour,
		meter:     func(context.Context, time.Time) { billed.Add(1) },
	}
	h.run(context.Background(), now)

	if probed.Load() != 0 {
		t.Fatalf("a non-owner probed the provider %d times, want 0", probed.Load())
	}
	if billed.Load() != 0 {
		t.Fatalf("a non-owner billed %d times, want 0", billed.Load())
	}
	// And the hour it did not claim is still there for the owner.
	if !object.ClaimMeterHour(now) {
		t.Fatal("a non-owner's skipped hour must still be claimable by the owner")
	}
}
