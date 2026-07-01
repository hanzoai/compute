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

import "testing"

// matchIssuer binds bearer tokens to THIS brand's IAM while staying multi-tenant
// (every org within the brand). These cases pin the brand isolation and the
// trailing-slash / comma-list tolerance that a prod IAM (which stamps the slashed
// form) requires.
func TestMatchIssuer(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		tokenIss   string
		want       bool
	}{
		{"exact", "https://hanzo.id", "https://hanzo.id", true},
		{"token slashed, config bare", "https://hanzo.id", "https://hanzo.id/", true},
		{"token bare, config slashed", "https://hanzo.id/", "https://hanzo.id", true},
		{"both slashed", "https://hanzo.id/", "https://hanzo.id/", true},
		{"case-insensitive", "https://Hanzo.ID", "https://hanzo.id", true},
		{"comma-list first", "https://hanzo.id,https://iam.hanzo.ai", "https://hanzo.id", true},
		{"comma-list second (spaces)", "https://hanzo.id, https://iam.hanzo.ai", "https://iam.hanzo.ai", true},
		{"cross-brand lux rejected", "https://hanzo.id", "https://lux.id", false},
		{"cross-brand zoo rejected", "https://hanzo.id", "https://zoo.id", false},
		{"empty config fails closed", "", "https://hanzo.id", false},
		{"empty token fails closed", "https://hanzo.id", "", false},
		{"whitespace-only config fails closed", "   ", "https://hanzo.id", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchIssuer(c.configured, c.tokenIss); got != c.want {
				t.Fatalf("matchIssuer(%q, %q) = %v, want %v", c.configured, c.tokenIss, got, c.want)
			}
		})
	}
}
