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

package object

import (
	"github.com/hanzoai/orm/relational/schemas"
	"github.com/hanzoai/compute/util"
)

// Fleet worker kinds — the BILLING LINEAGE of a connected compute source, resolved
// once at connect. It is exactly the fleet-billing tier the source belongs to:
//
//   - FleetWorkerBYOHardware: a bare-metal / GPU / device box NOT on a cloud →
//     flat $1/mo per connected device (DeviceCount), armed on connect and stopped
//     on disconnect.
//   - FleetWorkerValidator: a box whose validator address is in the hanzo.network
//     mainnet validator set → FREE / native (it already earns validator economics
//     and secures the chain), so it is never metered.
//
// A BYOC cloud ACCOUNT is a Provider, not a worker — it is billed at 1% of its
// cloud spend by the daily cost collector — so the worker registry is exactly the
// non-cloud tiers (b) and (c). One source, one kind, one rate.
const (
	FleetWorkerBYOHardware = "byo-hardware"
	FleetWorkerValidator   = "validator"
)

// Worker connection states.
const (
	FleetWorkerConnected    = "connected"
	FleetWorkerDisconnected = "disconnected"
)

// FleetWorker is a BYO compute box connected to the Hanzo cloud fleet (the
// `hanzo gpu connect` / desktop-link path). It is keyed by (Owner, Name) — Owner is
// the org boundary, Name a stable per-box id (hostname/uuid) — with Project as the
// second attribution dimension so device billing is per org+project. The device
// meter reads DeviceCount while State==connected; the validator exemption reads
// Kind + ValidatorAddress.
type FleetWorker struct {
	Owner string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name  string `xorm:"varchar(100) notnull pk" json:"name"`
	// Project is the attribution dimension alongside Owner (empty == default project).
	Project     string `xorm:"varchar(100)" json:"project"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`

	Kind        string `xorm:"varchar(50)" json:"kind"`      // FleetWorkerBYOHardware | FleetWorkerValidator
	DeviceCount int    `json:"deviceCount"`                  // GPUs/devices → the $1/mo multiplier (tier b)
	GpuModel    string `xorm:"varchar(100)" json:"gpuModel"` // informational (e.g. "H100")

	// ValidatorAddress is the box's hanzo.network validator address; when Kind is
	// FleetWorkerValidator it is matched against the on-chain validator set to grant
	// the free-tier exemption. Empty for a plain BYO-hardware box.
	ValidatorAddress string `xorm:"varchar(100)" json:"validatorAddress"`

	State    string `xorm:"varchar(50)" json:"state"` // FleetWorkerConnected | FleetWorkerDisconnected
	LastSeen string `xorm:"varchar(100)" json:"lastSeen"`
}

func (w *FleetWorker) GetId() string { return w.Owner + "/" + w.Name }

func getFleetWorker(owner, name string) (*FleetWorker, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	worker := FleetWorker{Owner: owner, Name: name}
	existed, err := adapter.engine.Get(&worker)
	if err != nil {
		return &worker, err
	}
	if existed {
		return &worker, nil
	}
	return nil, nil
}

// GetFleetWorkers lists an org's workers, newest first.
func GetFleetWorkers(owner string) ([]*FleetWorker, error) {
	workers := []*FleetWorker{}
	err := adapter.engine.Desc("created_time").Find(&workers, &FleetWorker{Owner: owner})
	return workers, err
}

// GetConnectedFleetWorkers returns every CONNECTED worker across ALL orgs — the set
// the monthly device meter bills. Like the running-machine sweep, it lists
// cross-tenant and recovers the org from each row, so one pass meters every org's
// connected devices. A disconnected worker is excluded (device fee stops on
// disconnect).
func GetConnectedFleetWorkers() ([]*FleetWorker, error) {
	workers := []*FleetWorker{}
	err := adapter.engine.Where("state = ?", FleetWorkerConnected).Find(&workers)
	return workers, err
}

// ConnectFleetWorker upserts a worker on connect (arming its billing), idempotent
// on (Owner, Name): a reconnect refreshes DeviceCount/Kind/LastSeen and flips State
// back to connected while preserving CreatedTime. It never fabricates identity —
// Owner/Name/Kind/DeviceCount are supplied by the caller (the connect handler,
// org-scoped and validated) and only timestamps + State are stamped here.
func ConnectFleetWorker(worker *FleetWorker) (*FleetWorker, error) {
	now := util.GetCurrentTime()
	worker.State = FleetWorkerConnected
	worker.LastSeen = now
	worker.UpdatedTime = now

	existing, err := getFleetWorker(worker.Owner, worker.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		worker.CreatedTime = existing.CreatedTime
		if _, err := adapter.engine.ID(schemas.PK{worker.Owner, worker.Name}).AllCols().Update(worker); err != nil {
			return nil, err
		}
		return worker, nil
	}
	worker.CreatedTime = now
	if _, err := adapter.engine.Insert(worker); err != nil {
		return nil, err
	}
	return worker, nil
}

// DisconnectFleetWorker marks a worker disconnected (stopping its device fee),
// returning whether a connected worker was present. The row is retained (not
// deleted) so its history and last DeviceCount survive for the closing month's
// bill; the meter simply stops counting it.
func DisconnectFleetWorker(owner, name string) (bool, error) {
	worker, err := getFleetWorker(owner, name)
	if err != nil || worker == nil {
		return false, err
	}
	worker.State = FleetWorkerDisconnected
	worker.UpdatedTime = util.GetCurrentTime()
	affected, err := adapter.engine.ID(schemas.PK{owner, name}).Cols("state", "updated_time").Update(worker)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
