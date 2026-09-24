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
	"sync"
	"time"
)

// Ledger keeps, per hosted machine, the last clock hour ("YYYYMMDDHH") its
// running time has been billed through. The launch, a start and the hourly sweep
// each advance it before they debit, and debit only the hours it moved over, so
// one hour is charged once whichever of them reaches it first, and hours a sweep
// missed — a restart, an outage, a failed tick — are charged by the next one.
//
// The rows live in the store (object), which service cannot import, so the
// store registers itself here the way it registers credentials.
type Ledger interface {
	// Through returns the hour machine is billed through, "" when it never was.
	Through(machine string) (string, error)
	// Advance moves each machine's mark forward to its hour, durably, and
	// reports which moved. A mark at or past its hour does not move: that hour
	// is already billed.
	Advance(marks map[string]string) (map[string]bool, error)
}

// book is the registered Ledger.
var (
	bookMu sync.RWMutex
	book   Ledger = newMemoryLedger()
)

// RegisterLedger installs where billed hours are kept. Unregistered, they are
// kept in this process only, which is what a test or a local run wants and what
// a deployment must not be left with: a restart forgets them.
func RegisterLedger(l Ledger) {
	bookMu.Lock()
	defer bookMu.Unlock()
	book = l
}

func billed() Ledger {
	bookMu.RLock()
	defer bookMu.RUnlock()
	return book
}

// memoryLedger is a Ledger held in this process.
type memoryLedger struct {
	mu    sync.Mutex
	marks map[string]string
}

func newMemoryLedger() *memoryLedger { return &memoryLedger{marks: map[string]string{}} }

func (l *memoryLedger) Through(machine string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.marks[machine], nil
}

func (l *memoryLedger) Advance(marks map[string]string) (map[string]bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	moved := map[string]bool{}
	for machine, hour := range marks {
		if l.marks[machine] >= hour {
			continue
		}
		l.marks[machine] = hour
		moved[machine] = true
	}
	return moved, nil
}

// hourOf is the clock hour t falls in, as a ledger mark.
func hourOf(t time.Time) string { return HourStamp(t) }

// parseHour reads a ledger mark back into the start of its hour.
func parseHour(stamp string) (time.Time, bool) {
	t, err := time.Parse("2006010215", stamp)
	return t, err == nil
}
