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

package routers

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/controllers"
	"github.com/hanzoai/compute/object"
)

// ownedKind is one org-owned collection: its address, how to seed a row, and how
// to read one back as JSON ("" when absent).
type ownedKind struct {
	kind   string
	seed   func(owner, name string) error
	read   func(owner, name string) (string, error)
	add    func(*controllers.ApiController)
	update func(*controllers.ApiController)
	remove func(*controllers.ApiController)
}

func present(v any, err error) (string, error) {
	if err != nil || v == nil || reflect.ValueOf(v).IsNil() {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func ownedKinds() []ownedKind {
	return []ownedKind{
		{
			kind: "providers",
			seed: func(o, n string) error {
				_, err := object.AddProvider(&object.Provider{Owner: o, Name: n, Category: "Public Cloud", Type: "Amazon Web Services", DisplayName: "seed"})
				return err
			},
			read:   func(o, n string) (string, error) { return present(object.GetProvider(o + "/" + n)) },
			add:    (*controllers.ApiController).AddProvider,
			update: (*controllers.ApiController).UpdateProvider,
			remove: (*controllers.ApiController).DeleteProvider,
		},
		{
			kind: "assets",
			seed: func(o, n string) error {
				_, err := object.AddAsset(&object.Asset{Owner: o, Name: n, DisplayName: "seed"})
				return err
			},
			read:   func(o, n string) (string, error) { return present(object.GetAsset(o + "/" + n)) },
			add:    (*controllers.ApiController).AddAsset,
			update: (*controllers.ApiController).UpdateAsset,
			remove: (*controllers.ApiController).DeleteAsset,
		},
		{
			kind: "records",
			seed: func(o, n string) error {
				object.AddRecord(&object.Record{Organization: o, Name: n, Method: "POST", Action: "seed"})
				return nil
			},
			read:   func(o, n string) (string, error) { return present(object.GetRecord(o + "/" + n)) },
			add:    (*controllers.ApiController).AddRecord,
			update: (*controllers.ApiController).UpdateRecord,
			remove: (*controllers.ApiController).DeleteRecord,
		},
		{
			kind: "sessions",
			seed: func(o, n string) error {
				_, err := object.AddSession(&object.Session{Owner: o, Name: n, Protocol: "ssh"})
				return err
			},
			read:   func(o, n string) (string, error) { return present(object.GetConnSession(o + "/" + n)) },
			add:    (*controllers.ApiController).AddSession,
			update: (*controllers.ApiController).UpdateSession,
			remove: (*controllers.ApiController).DeleteSession,
		},
		{
			kind: "plans",
			seed: func(o, n string) error {
				_, err := object.AddPlan(&object.Plan{Owner: o, Name: n, DisplayName: "seed"})
				return err
			},
			read:   func(o, n string) (string, error) { return present(object.GetPlan(o, n)) },
			add:    (*controllers.ApiController).AddPlan,
			update: (*controllers.ApiController).UpdatePlan,
			remove: (*controllers.ApiController).DeletePlan,
		},
	}
}

// A write lands on the caller's own org and the address's name, whatever the body
// says: a tenant cannot create, change or delete a row of the admin org, or of any
// org other than its own, through any org-owned collection, while a member of the
// admin org writes admin rows.
func TestAWriteLandsOnTheCallersOwnRow(t *testing.T) {
	redStore.Do(func() {
		root, _ := os.MkdirTemp("", "red3")
		_ = os.Setenv("dataRoot", root)
		object.InitAdapter()
	})
	for _, k := range ownedKinds() {
		t.Run(k.kind, func(t *testing.T) {
			app := zip.New(zip.Config{ReadBufferSize: 16384})
			app.Use(zip.H(ApiFilter))
			app.Post("/v1/"+k.kind, h(k.add))
			app.Put("/v1/"+k.kind+"/:owner/:name", h(k.update))
			app.Delete("/v1/"+k.kind+"/:owner/:name", h(k.remove))
			send := func(org, method, path, body string) {
				t.Helper()
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Authorization", redSign(t, org))
				req.Header.Set("Content-Type", "application/json")
				if _, err := app.Test(req); err != nil {
					t.Fatal(err)
				}
			}
			row := func(owner, name string) string {
				t.Helper()
				got, err := k.read(owner, name)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}

			if err := k.seed("admin", "seed"); err != nil {
				t.Fatal(err)
			}
			if err := k.seed("mallory", "seed"); err != nil {
				t.Fatal(err)
			}
			before := row("admin", "seed")
			if before == "" {
				t.Fatal("the admin row was not seeded")
			}

			send("mallory", "POST", "/v1/"+k.kind+"?id=mallory/evil", `{"owner":"admin","name":"evil"}`)
			if row("admin", "evil") != "" {
				t.Errorf("a tenant created admin/evil")
			}
			send("mallory", "PUT", "/v1/"+k.kind+"/mallory/seed", `{"owner":"admin","name":"seed","displayName":"pwned","protocol":"pwned","action":"pwned"}`)
			if after := row("admin", "seed"); after != before {
				t.Errorf("a tenant changed admin/seed:\n%s\n%s", before, after)
			}
			send("mallory", "DELETE", "/v1/"+k.kind+"/mallory/anything", `{"owner":"admin","name":"seed"}`)
			send("mallory", "DELETE", "/v1/"+k.kind+"/mallory/seed", ``)
			if row("admin", "seed") == "" {
				t.Errorf("a tenant deleted admin/seed")
			}
			if row("mallory", "seed") != "" {
				t.Errorf("the tenant's own delete of mallory/seed did not land")
			}

			send("mallory", "POST", "/v1/"+k.kind, `{"owner":"mallory","name":"mine"}`)
			if row("mallory", "mine") == "" {
				t.Errorf("the tenant's own create of mallory/mine did not land")
			}
			send("admin", "POST", "/v1/"+k.kind, `{"owner":"admin","name":"platform"}`)
			if row("admin", "platform") == "" {
				t.Errorf("a member of admin could not create admin/platform")
			}
		})
	}
}

// The service caller authenticates by Basic with this service's own client id
// and secret. The same pair in the query string, or the secret under another
// client id, is no subject at all.
func TestTheServiceCallerIsBasicWithItsOwnId(t *testing.T) {
	t.Setenv("clientId", "hanzo-visor")
	t.Setenv("clientSecret", "s3cret")
	t.Setenv("iamApplication", "visor")
	app := zip.New(zip.Config{})
	var got string
	app.Get("/v1/x", func(c *zip.Ctx) error {
		got, _ = getUsernameByClientIdSecret(c)
		return nil
	})
	for _, tc := range []struct {
		name, query, basic, want string
	}{
		{"Basic with its own pair", "", "hanzo-visor:s3cret", "app/visor"},
		{"the pair in the query", "?clientId=hanzo-visor&clientSecret=s3cret", "", ""},
		{"the secret under another id", "", "other-app:s3cret", ""},
		{"a wrong secret", "", "hanzo-visor:guess", ""},
	} {
		req := httptest.NewRequest("GET", "/v1/x"+tc.query, nil)
		if tc.basic != "" {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(tc.basic)))
		}
		got = "unset"
		if _, err := app.Test(req); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s: subject %q, want %q", tc.name, got, tc.want)
		}
	}
}
