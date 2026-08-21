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
	"testing"

	"github.com/digitalocean/godo"
)

// godo makes Region, Size and Image pointers. A droplet answered without them —
// a cloud mid-incident, a page with a partial body — used to panic, and a panic
// in this process is every tenant's listing, not one droplet's fields.
func TestADropletMissingItsFieldsDoesNotPanic(t *testing.T) {
	machine := getMachineFromDroplet(godo.Droplet{ID: 7, Name: "web-1", Status: "active"})
	if machine == nil {
		t.Fatal("no machine")
	}
	if machine.DisplayName != "web-1" || machine.Id != "7" {
		t.Errorf("what WAS there was lost: %+v", machine)
	}
	for name, got := range map[string]string{"region": machine.Region, "size": machine.Size, "image": machine.Image} {
		if got != "" {
			t.Errorf("%s = %q, want empty — nothing was there to read", name, got)
		}
	}
}
