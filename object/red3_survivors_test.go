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

package object

import (
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/compute/service"
)

// Kills M1d: only the reserved admin org's rows are platform accounts. A row of
// any other org — here "hanzo", a brand org — reaches its cloud as that tenant,
// which the carrier refuses, and never as the platform's own account.
func TestRedOnlyAdminRowsArePlatformAccounts(t *testing.T) {
	installBaseStore(t)
	for _, owner := range []string{"hanzo", "admin"} {
		if _, err := AddProvider(&Provider{Owner: owner, Name: "aws", Category: "Public Cloud", Type: "AWS", State: "Active"}); err != nil {
			t.Fatal(err)
		}
	}
	service.RegisterCarrier(func(service.Credential) (*http.Client, error) { return http.DefaultClient, nil })
	t.Cleanup(func() { service.RegisterCarrier(nil) })

	brand, err := GetProvider("hanzo/aws")
	if err != nil || brand == nil {
		t.Fatal(err)
	}
	if c := brand.credential(LaunchCredential{Region: "us-east-1"}); c.Tenant != "hanzo" {
		t.Fatalf("org hanzo's provider row is a platform account: %+v", c)
	}
	if _, err := service.NewMachineClient(brand.credential(LaunchCredential{Region: "us-east-1"})); !errors.Is(err, service.ErrTenantNotCarried) {
		t.Fatalf("org hanzo's provider row reached its cloud: %v", err)
	}
	platform, err := GetProvider("admin/aws")
	if err != nil || platform == nil {
		t.Fatal(err)
	}
	if c := platform.credential(LaunchCredential{Region: "us-east-1"}); c.Tenant != "" {
		t.Fatalf("admin's provider row is not the platform's: %+v", c)
	}
}
