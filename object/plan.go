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

import "xorm.io/core"

// Plan represents a resale VM plan available to customers.
type Plan struct {
	Owner       string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name        string `xorm:"varchar(100) notnull pk" json:"name"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`

	DisplayName string `xorm:"varchar(100)" json:"displayName"`
	Description string `xorm:"varchar(500)" json:"description"`
	Category    string `xorm:"varchar(100)" json:"category"` // "bot", "standard", "pro", "power"
	State       string `xorm:"varchar(100)" json:"state"`    // "Active", "Inactive"

	// Specs advertised to the customer
	VCpu    int    `json:"vCpu"`
	Ram     int    `json:"ram"`                        // MB
	Disk    int    `json:"disk"`                       // GB
	CpuType string `xorm:"varchar(50)" json:"cpuType"` // "shared", "dedicated"

	// Pricing (cents/mo)
	PriceMonthly int `json:"priceMonthly"` // customer price in cents USD

	// Region availability
	Regions string `xorm:"varchar(500)" json:"regions"` // comma-separated: "us,eu,sg"

	// Provider mapping per region (JSON)
	// e.g. {"us":{"provider":"Hetzner","serverType":"cpx31","location":"ash"},...}
	ProviderMapping string `xorm:"mediumtext" json:"providerMapping"`

	// Traffic included (GB)
	TrafficIncluded int `json:"trafficIncluded"`

	SortOrder int `json:"sortOrder"`
}

// Plan is a global read-only catalog: identical for every org, so it lives on
// the shared engine (Shared(), Postgres under both backends), never duplicated
// into per-org SQLite. The owner column is retained so white-label brands can
// scope their own catalog, but the physical table is shared. All Plan CRUD
// therefore routes through Shared().
func GetPlans(owner string) ([]*Plan, error) {
	plans := []*Plan{}
	err := Shared().Where("owner = ? AND state = ?", owner, "Active").Asc("sort_order").Find(&plans)
	if err != nil {
		return nil, err
	}
	return plans, nil
}

func GetAllPlans(owner string) ([]*Plan, error) {
	plans := []*Plan{}
	err := Shared().Where("owner = ?", owner).Asc("sort_order").Find(&plans)
	if err != nil {
		return nil, err
	}
	return plans, nil
}

func GetPlan(owner string, name string) (*Plan, error) {
	plan := Plan{Owner: owner, Name: name}
	existed, err := Shared().Get(&plan)
	if err != nil {
		return nil, err
	}
	if !existed {
		return nil, nil
	}
	return &plan, nil
}

func AddPlan(plan *Plan) (bool, error) {
	affected, err := Shared().Insert(plan)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func UpdatePlan(owner string, name string, plan *Plan) (bool, error) {
	_, err := Shared().ID(core.PK{owner, name}).AllCols().Update(plan)
	if err != nil {
		return false, err
	}
	return true, nil
}

func DeletePlan(plan *Plan) (bool, error) {
	affected, err := Shared().ID(core.PK{plan.Owner, plan.Name}).Delete(&Plan{})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
