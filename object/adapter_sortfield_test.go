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
	"fmt"
	"strings"
	"testing"

	"github.com/hanzoai/orm/relational"
)

// sortFieldPayloads are values a caller can put in ?sortField= or ?field=. Each
// is lowercase on purpose: SnakeString lowercases and inserts "_" before
// capitals, so only a lowercase payload survives it byte for byte.
//
// The backtick ones are the payloads that actually execute on this stack, and
// they are why this file exists. xorm hands the column to
// schemas.Quoter.QuoteTo, which writes Prefix + word + Suffix around the WHOLE
// word, so a parenthesis alone does NOT escape — "(select 1)" comes back as the
// quoted identifier `(select 1)` and errors with "no such column". A backtick
// inside the word closes the quote the writer opened, and everything after it is
// SQL. Measured against the real builder on the real driver:
//
//	sortField = name`,(select/**/group_concat(name)/**/from/**/sqlite_master)--
//	ORDER BY    `name`,(select/**/group_concat(name)/**/from/**/sqlite_master)--` ASC
//
// which ran clean, no error, before util.FilterField was applied to sortField.
// "/**/" stands in for the spaces the writer eats, and the trailing "--"
// swallows the closing quote the writer still owes. The same break-out works on
// Postgres with a double quote — postgresQuoter is the same shape with '"'.
var sortFieldPayloads = []string{
	"name`,(select/**/group_concat(name)/**/from/**/sqlite_master)--",
	"name`,iif((select/**/1)=1,owner,name)--",
	"name`--",
	`name",(select/**/1)--`,
	"(select/**/name/**/from/**/session)",
	"iif(1,name,owner)",
	"name/**/desc,(select/**/1)",
	"name;drop/**/table/**/session",
	"name\ndesc",
}

// emitted runs the session and returns the statement that reached the driver.
// The assertions below compare emitted statements to each other rather than to a
// hardcoded string, so they stay exact while surviving a schema change: adding a
// Session column moves both sides identically.
func emitted(s *relational.Session) (string, string) {
	rows := []*Session{}
	_ = s.Find(&rows, &Session{})
	sql, args := s.LastSQL()
	return sql, fmt.Sprint(args)
}

// TestGetSession_SortFieldCannotReachOrderBy asserts on the SQL the REAL builder
// emits against the REAL driver, not on the whitelist in isolation, so it fails
// if the quoting underneath ever changes shape.
func TestGetSession_SortFieldCannotReachOrderBy(t *testing.T) {
	installBaseStore(t)

	for _, order := range []string{"ascend", "descend"} {
		// The default the guard falls back to, reached the legitimate way.
		want, wantArgs := emitted(GetSession("org-a", 0, 10, "", "", "createdTime", order))
		if !strings.Contains(want, "ORDER BY `created_time`") {
			t.Fatalf("baseline is not the default sort: %s", want)
		}
		for _, payload := range sortFieldPayloads {
			got, gotArgs := emitted(GetSession("org-a", 0, 10, "", "", payload, order))
			if got != want || gotArgs != wantArgs {
				t.Errorf("sortField=%q order=%s reached the statement:\n got  %s %s\n want %s %s",
					payload, order, got, gotArgs, want, wantArgs)
			}
		}
	}
}

// TestGetSession_FilterFieldCannotReachWhere pins the sibling the sort column was
// modelled on. Both are caller-supplied identifiers and both must be whitelisted;
// pinning only one is how the asymmetry got there in the first place.
func TestGetSession_FilterFieldCannotReachWhere(t *testing.T) {
	installBaseStore(t)

	want, wantArgs := emitted(GetSession("org-a", 0, 10, "", "", "", ""))
	for _, payload := range sortFieldPayloads {
		got, gotArgs := emitted(GetSession("org-a", 0, 10, payload, "x", "", ""))
		if got != want || gotArgs != wantArgs {
			t.Errorf("field=%q reached the statement:\n got  %s %s\n want %s %s",
				payload, got, gotArgs, want, wantArgs)
		}
	}
}

// TestGetSession_LegitimateSortFieldsStillSort keeps the whitelist honest: the UI
// sends Ant Design dataIndex names, which are alphanumeric, and each must still
// reach ORDER BY as its snake_case column with its direction intact.
func TestGetSession_LegitimateSortFieldsStillSort(t *testing.T) {
	installBaseStore(t)

	for field, col := range map[string]string{
		"createdTime":   "created_time",
		"name":          "name",
		"owner":         "owner",
		"status":        "status",
		"connectedTime": "connected_time",
	} {
		for order, dir := range map[string]string{"ascend": "ASC", "descend": "DESC"} {
			sql, _ := emitted(GetSession("org-a", 0, 10, "", "", field, order))
			want := "ORDER BY `" + col + "` " + dir
			if !strings.Contains(sql, want) {
				t.Errorf("sortField=%q order=%s should emit %q, SQL = %s", field, order, want, sql)
			}
		}
	}
}

// TestGetSession_LegitimateFilterFieldStillFilters is the same honesty check for
// the filter column: an alphanumeric field must still build its LIKE, with the
// value bound rather than concatenated.
func TestGetSession_LegitimateFilterFieldStillFilters(t *testing.T) {
	installBaseStore(t)

	sql, args := emitted(GetSession("org-a", 0, 10, "displayName", "acme", "", ""))
	if !strings.Contains(sql, "display_name like ?") {
		t.Errorf("field=displayName should build a bound LIKE, SQL = %s", sql)
	}
	if !strings.Contains(args, "%acme%") {
		t.Errorf("filter value should be a bound argument, args = %s", args)
	}
}
