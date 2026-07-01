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

package controllers

import "testing"

func TestPriceToCents(t *testing.T) {
	cases := []struct {
		price float64
		want  int64
	}{
		{0.07, 7},       // whole-cent: float overshoot must NOT ceil to 8
		{0.29, 29},      // whole-cent
		{33.60, 3360},   // monthly-scale whole cents
		{4.2375, 424},   // H100/hr -> 423.75 -> 424
		{0.04999, 5},    // 4.999 -> 5
		{0.00833, 1},    // true sub-cent still gates on >= 1
		{0.0, 0},        // free -> 0 (no gate)
		{-1.0, 0},       // guard: negative -> 0
	}
	for _, c := range cases {
		if got := priceToCents(c.price); got != c.want {
			t.Fatalf("priceToCents(%v) = %d, want %d", c.price, got, c.want)
		}
	}
}
