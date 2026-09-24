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

package object

import (
	"time"

	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/service"
)

// MeterMark is how far one ledger key — a hosted machine's running hours, its
// stopped disk, an org's balance reads — has come: service.Ledger, kept on the
// shared coord engine beside the hour leases, so it survives a restart and a new
// owner resumes from it. See service/ledger.go for what reads and moves it.
type MeterMark struct {
	Machine     string `xorm:"varchar(100) notnull pk" json:"machine"`
	Hour        string `xorm:"varchar(12)" json:"hour"` // UTC "YYYYMMDDHH"
	Streak      int64  `json:"streak"`                  // length of a run ending at Hour
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`
}

// meterLedger is service.Ledger over MeterMark.
type meterLedger struct{}

// RegisterMeterLedger makes the shared store where billed hours are kept.
func RegisterMeterLedger() { service.RegisterLedger(meterLedger{}) }

func (meterLedger) Through(key string) (service.Mark, error) {
	mark := MeterMark{Machine: key}
	if _, err := Shared().Get(&mark); err != nil {
		return service.Mark{}, err
	}
	return service.Mark{Hour: mark.Hour, Streak: mark.Streak}, nil
}

// Advance writes every mark that moves, then ships the coord DB once, so the
// marks are durable before the caller debits. A ship that fails is logged and
// not returned: the marks are on this pod's disk, and a debit's id names its
// machine and hour, which the ledger debits once, so a pod resuming from an
// older copy re-sends ids already charged rather than charging them again.
func (meterLedger) Advance(marks map[string]service.Mark) (map[string]bool, error) {
	moved := map[string]bool{}
	now := time.Now().UTC().Format(time.RFC3339)
	for key, to := range marks {
		mark := MeterMark{Machine: key}
		existed, err := Shared().Get(&mark)
		if err != nil {
			return moved, err
		}
		if existed && mark.Hour >= to.Hour {
			continue
		}
		mark.Hour, mark.Streak, mark.UpdatedTime = to.Hour, to.Streak, now
		if existed {
			_, err = Shared().ID(key).Cols("hour", "streak", "updated_time").Update(&mark)
		} else {
			_, err = Shared().Insert(&mark)
		}
		if err != nil {
			return moved, err
		}
		moved[key] = true
	}
	if len(moved) > 0 {
		if err := pushShared(); err != nil {
			logs.Warning("compute metering: ship billed hours: %v", err)
		}
	}
	return moved, nil
}
