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

import "testing"

// TestGetSessionsAnswers is a list that could not answer at all: it sorted by
// `connected_time`, a column the Session model lost when ConnectedTime was
// renamed to StartTime, so every unpaginated read came back "no such column"
// rather than a session. Only the PAGINATED branch was ever exercised — it sorts
// through GetSession — which is why a whole endpoint could be dead and nothing
// said so.
//
// Ordering is asserted too, because sorting by a column that exists is only half
// of it: newest first is what the list means.
func TestGetSessionsAnswers(t *testing.T) {
	installBaseStore(t)

	for _, s := range []*Session{
		{Owner: "acme", Name: "older", Asset: "web", Status: NoConnect, StartTime: "2026-01-01T00:00:00Z"},
		{Owner: "acme", Name: "newer", Asset: "web", Status: NoConnect, StartTime: "2026-02-01T00:00:00Z"},
		{Owner: "other", Name: "theirs", Asset: "db", Status: NoConnect, StartTime: "2026-03-01T00:00:00Z"},
	} {
		if _, err := AddSession(s); err != nil {
			t.Fatalf("AddSession(%s/%s): %v", s.Owner, s.Name, err)
		}
	}

	got, err := GetSessions("acme")
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetSessions returned %d sessions, want the org's 2", len(got))
	}
	if got[0].Name != "newer" {
		t.Fatalf("GetSessions[0] = %q, want the newest first", got[0].Name)
	}
}
