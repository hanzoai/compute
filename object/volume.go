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

// Volume represents a block storage volume attached to a machine.
type Volume struct {
	Owner string `xorm:"varchar(100) notnull pk" json:"owner"`
	Name  string `xorm:"varchar(100) notnull pk" json:"name"`
	// Project is the attribution dimension alongside Owner: the project WITHIN the
	// org that owns this volume. Additive, Sync2-safe, defaults to "".
	Project     string `xorm:"varchar(100)" json:"project"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`

	DisplayName string `xorm:"varchar(100)" json:"displayName"`
	Id          string `xorm:"varchar(100)" json:"id"`       // Cloud provider volume ID
	Provider    string `xorm:"varchar(100)" json:"provider"` // Provider name
	Machine     string `xorm:"varchar(100)" json:"machine"`  // Attached machine name (empty if detached)
	Region      string `xorm:"varchar(100)" json:"region"`
	Size        int    `json:"size"`                           // GB
	State       string `xorm:"varchar(100)" json:"state"`      // "Available", "Attached", "Creating"
	Format      string `xorm:"varchar(50)" json:"format"`      // "ext4", "xfs", etc.
	MountPoint  string `xorm:"varchar(200)" json:"mountPoint"` // e.g. "/mnt/data"
}

func (v *Volume) GetId() string {
	return v.Owner + "/" + v.Name
}

func GetVolumes(owner string) ([]*Volume, error) {
	engine, err := EngineFor(owner)
	if err != nil {
		return nil, err
	}
	volumes := []*Volume{}
	err = engine.Where("owner = ?", owner).Desc("created_time").Find(&volumes)
	if err != nil {
		return nil, err
	}
	return volumes, nil
}

func GetVolume(owner string, name string) (*Volume, error) {
	engine, err := EngineFor(owner)
	if err != nil {
		return nil, err
	}
	volume := Volume{Owner: owner, Name: name}
	existed, err := engine.Get(&volume)
	if err != nil {
		return nil, err
	}
	if !existed {
		return nil, nil
	}
	return &volume, nil
}

func AddVolume(volume *Volume) (bool, error) {
	engine, err := EngineFor(volume.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Insert(volume)
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func UpdateVolume(owner string, name string, volume *Volume) (bool, error) {
	engine, err := EngineFor(owner)
	if err != nil {
		return false, err
	}
	_, err = engine.ID([]any{owner, name}).AllCols().Update(volume)
	if err != nil {
		return false, err
	}
	return true, nil
}

func DeleteVolume(volume *Volume) (bool, error) {
	engine, err := EngineFor(volume.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.ID([]any{volume.Owner, volume.Name}).Delete(&Volume{})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
