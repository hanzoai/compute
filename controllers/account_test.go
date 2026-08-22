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

package controllers

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestAccountRefusesWithoutACredential: the outcome is the STATUS.
//
// The address this op replaced answered 200 carrying
// {"status":"error","msg":"please sign in first"}, so a caller learned it had
// failed only by reading a field of a success — which is the kind of check a
// client omits once and never notices.
func TestAccountRefusesWithoutACredential(t *testing.T) {
	for _, header := range []string{"", "Bearer ", "Basic Zm9v", "nonsense"} {
		out, err := GetAccount(context.Background(), &Credential{Authorization: header})
		if out != nil {
			t.Errorf("Authorization %q answered an account", header)
		}
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
			t.Errorf("Authorization %q = %v, want 401", header, err)
		}
	}
}

// TestAccountCarriesNoCredential is the reason the answer is a narrow value
// rather than the decoded claim set.
//
// The old address answered with the whole of iamsdk.Claims, so it handed the
// caller back the access token it had just sent, beside the password fields
// iamsdk.User declares. A body travels further than the request that produced
// it; the account says who you are and nothing about how you proved it.
func TestAccountCarriesNoCredential(t *testing.T) {
	for _, name := range []string{"accessToken", "password", "passwordSalt", "passwordType"} {
		if _, found := reflect.TypeOf(Account{}).FieldByNameFunc(func(f string) bool {
			return strings.EqualFold(f, name)
		}); found {
			t.Errorf("Account carries %s — the identity is not the credential", name)
		}
	}
}

// TestAccountReadsOnlyTheCredential: an account is never another org's to ask
// for, so this op takes no owner to be asked with.
//
// Scope carries one (a service subject scoping a LIST), and reusing it here
// would publish a parameter the op must ignore — a documented lie — and turn
// "who am I" into a user lookup, which belongs to IAM.
func TestAccountReadsOnlyTheCredential(t *testing.T) {
	in := reflect.TypeOf(Credential{})
	if n := in.NumField(); n != 1 {
		t.Fatalf("Credential has %d fields, want 1 (the Bearer)", n)
	}
	f := in.Field(0)
	if got := f.Tag.Get("header"); got != "Authorization" {
		t.Errorf("Credential field %s reads header %q, want Authorization", f.Name, got)
	}
	if got := f.Tag.Get("json"); got != "-" {
		t.Errorf("Credential field %s json tag = %q, want \"-\": it rides the header, never a body", f.Name, got)
	}
}
