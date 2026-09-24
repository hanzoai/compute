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

package routers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/iamsdk/v2/iamsdk"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/controllers"
	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
)

var redStore sync.Once

func redSign(t *testing.T, org string) string {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	iamsdk.InitConfig("", "", "", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "", "")
	t.Setenv("iamIssuer", "https://test.id")
	t.Setenv("iamAudience", "hanzo-visor")
	c := &iamsdk.Claims{}
	c.Owner, c.Name = "hanzo", "mallory"
	c.Issuer = "https://test.id"
	c.Audience = []string{"hanzo-visor"}
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	c.Orgs = []iamsdk.OrgRef{{Org: org, Role: "owner"}}
	s, _ := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
	return "Bearer " + s
}

// A tenant writes a provider row owned by the reserved admin org: the filter
// authorizes the ?id owner, the handler inserts the body owner.
func TestRedTenantWritesAnAdminProviderRow(t *testing.T) {
	redStore.Do(func() {
		root, _ := os.MkdirTemp("", "red3")
		_ = os.Setenv("dataRoot", root)
		object.InitAdapter()
	})
	bearer := redSign(t, "mallory")
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	app.Use(zip.H(ApiFilter))
	app.Post("/v1/providers", h((*controllers.ApiController).AddProvider))

	body := `{"owner":"admin","name":"do-evil","category":"Public Cloud","type":"DigitalOcean","clientSecret":"dop_v1_attacker","state":"Active"}`
	for _, q := range []string{"", "?id=mallory/do-evil"} {
		req := httptest.NewRequest("POST", "/v1/providers"+q, strings.NewReader(body))
		req.Header.Set("Authorization", bearer)
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		t.Logf("POST /v1/providers%s -> %d %s", q, resp.StatusCode, b)
	}
	rows, err := object.GetProviders(service.SuperAdminOrg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rows {
		if p.Name == "do-evil" {
			t.Errorf("mallory wrote admin's provider row %s/%s (type %s): it is now a platform account", p.Owner, p.Name, p.Type)
		}
	}
}

// A tenant deletes the platform's provider row: the filter authorizes the path
// owner, the handler deletes the body's (owner, name).
func TestRedTenantDeletesAnAdminProviderRow(t *testing.T) {
	redStore.Do(func() {
		root, _ := os.MkdirTemp("", "red3")
		_ = os.Setenv("dataRoot", root)
		object.InitAdapter()
	})
	if _, err := object.AddProvider(&object.Provider{Owner: "admin", Name: "do-prod", Type: "DigitalOcean", Category: "Public Cloud", State: "Active"}); err != nil {
		t.Fatal(err)
	}
	bearer := redSign(t, "mallory")
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	app.Use(zip.H(ApiFilter))
	app.Delete("/v1/providers/:owner/:name", h((*controllers.ApiController).DeleteProvider))
	req := httptest.NewRequest("DELETE", "/v1/providers/mallory/anything", strings.NewReader(`{"owner":"admin","name":"do-prod"}`))
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	t.Logf("DELETE -> %d %s", resp.StatusCode, b)
	rows, _ := object.GetProviders("admin")
	for _, p := range rows {
		if p.Name == "do-prod" {
			return
		}
	}
	t.Errorf("mallory deleted admin/do-prod, the platform's account")
}
