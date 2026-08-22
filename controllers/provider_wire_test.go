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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// These pin the WIRE of the provider ops against a real store (TestMain's Base
// backend at a temp dataRoot), because a provider holds an org's cloud
// credentials and the two properties that matter — where the address is read
// from, and whether a save can destroy a secret — are invisible to the compiler.
//
// Addresses are pinned separately, by routers/router_contract_test.go.

// providerWire stands the five ops up on a bare app, at the paths registerProvider
// uses. No filter chain: the question here is what a HANDLER answers.
func providerWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/providers", ListProviders)
	zip.Post(app, "/v1/providers", AddProvider)
	zip.Get(app, "/v1/providers/:owner/:name", GetProvider)
	zip.Put(app, "/v1/providers/:owner/:name", ReplaceProvider)
	zip.Delete(app, "/v1/providers/:owner/:name", RemoveProvider)
	return app
}

// send drives one real request, with a body when there is one.
func send(t *testing.T, app *zip.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res.StatusCode, string(b)
}

// stored reads a provider straight out of the store, UNMASKED — the only way to
// see what a write actually left behind.
func stored(t *testing.T, owner, name string) *object.Provider {
	t.Helper()
	p, err := object.GetProvider(owner + "/" + name)
	if err != nil {
		t.Fatalf("read %s/%s: %v", owner, name, err)
	}
	return p
}

// TestProviderCreateAnswersTheMaskedRecord pins that a create answers the thing
// it created and that the answer never carries the credential back out. The old
// wire answered the word "Affected" inside a 200 envelope, which told a caller
// neither what was stored nor where it now lives.
func TestProviderCreateAnswersTheMaskedRecord(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	status, body := send(t, app, http.MethodPost, "/v1/providers",
		`{"owner":"`+mine+`","name":"do","type":"DigitalOcean","clientSecret":"dop_v1_real","region":"sfo3"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/providers = %d %s, want 200", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("POST /v1/providers answered the casibase envelope: %s", body)
	}

	var got object.Provider
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got.ClientSecret != "***" {
		t.Fatalf("create answered clientSecret %q, want it masked", got.ClientSecret)
	}
	// The control: the credential really was stored, so the mask is a mask and
	// not a write that lost the secret.
	if p := stored(t, mine, "do"); p == nil || p.ClientSecret != "dop_v1_real" {
		t.Fatalf("stored clientSecret = %v, want the real one", p)
	}
}

// TestProviderReadOfAnAbsentOneIs404. The old wire answered 200 with `data:
// null`, so "this org holds no such provider" and "here it is" were the same
// status, told apart only by looking inside.
func TestProviderReadOfAnAbsentOneIs404(t *testing.T) {
	app := providerWire(t)

	status, body := send(t, app, http.MethodGet, "/v1/providers/"+org(t)+"/nothing", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET an absent provider = %d %s, want 404", status, body)
	}
	if envelope(t, body) {
		t.Fatalf("the miss answered the casibase envelope: %s", body)
	}
}

// TestProviderListIsAnObjectKeyedProviders pins the collection shape. A bare
// array would decode just as well in a client, and that is the point of
// asserting the key: a collection that answers an array has nowhere to put the
// total, which the paged read has to carry.
func TestProviderListIsAnObjectKeyedProviders(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{Owner: mine, Name: "do", Type: "DigitalOcean", ClientSecret: "dop_v1_real"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	status, body := send(t, app, http.MethodGet, "/v1/providers?owner="+mine, "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/providers = %d %s, want 200", status, body)
	}
	var got Providers
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got.Providers) != 1 || got.Total != 1 {
		t.Fatalf("list = %d rows / total %d in %s, want 1 / 1", len(got.Providers), got.Total, body)
	}
	if got.Providers[0].ClientSecret != "***" {
		t.Fatalf("list answered an unmasked secret: %s", body)
	}

	// An org holding nothing answers an empty ARRAY, never null: a client that
	// iterates a collection should not have to test it for absence first.
	_, empty := send(t, app, http.MethodGet, "/v1/providers?owner="+mine+"-none", "")
	if !strings.Contains(empty, `"providers":[]`) {
		t.Fatalf("an empty collection answered %s, want providers: []", empty)
	}
}

// TestProviderReplaceStaysInTheAddressedOrg is the load-bearing one. The address
// is the authority on the TENANT: a body naming another owner cannot reach into
// that org, and cannot mint a row there either — which is the property the
// authorization seam rests on, since it authorized the address and not the body.
func TestProviderReplaceStaysInTheAddressedOrg(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{Owner: mine, Name: "do", Type: "DigitalOcean", Region: "sfo3"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	status, body := send(t, app, http.MethodPut, "/v1/providers/"+mine+"/do",
		`{"provider":{"owner":"someone-else","name":"do","type":"DigitalOcean","region":"nyc3"}}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", status, body)
	}

	if p := stored(t, mine, "do"); p == nil || p.Region != "nyc3" || p.Owner != mine {
		t.Fatalf("the addressed provider was not replaced in its own org: %v", p)
	}
	if p := stored(t, "someone-else", "do"); p != nil {
		t.Fatalf("the body's owner minted a row at someone-else/do: %v", p)
	}
}

// TestProviderReplaceRenames pins the flow that names a provider at all: one is
// created under a generated name (provider_<random>) and given a real one by
// editing it. The path says which row is being written and the record says what
// it becomes, so the two names are two values — flattened into one they would
// collapse and a rename would silently do nothing.
func TestProviderReplaceRenames(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{Owner: mine, Name: "provider_kx3", Type: "DigitalOcean", Region: "sfo3"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	status, body := send(t, app, http.MethodPut, "/v1/providers/"+mine+"/provider_kx3",
		`{"provider":{"name":"do","type":"DigitalOcean","region":"sfo3"}}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", status, body)
	}
	// The answer is read back from where the provider now lives.
	var got object.Provider
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got.Name != "do" {
		t.Fatalf("the answer names %q, want the new name", got.Name)
	}
	if p := stored(t, mine, "do"); p == nil {
		t.Fatal("the rename did not land")
	}
	if p := stored(t, mine, "provider_kx3"); p != nil {
		t.Fatalf("the old name still resolves: %v", p)
	}
}

// TestProviderReplaceWithNoNameKeepsTheAddressed pins the other half of the same
// rule. The record is written with ALL columns, so a body carrying no name would
// blank the primary key of the row it was meant to keep — leaving a provider
// nothing can address and nothing can delete.
func TestProviderReplaceWithNoNameKeepsTheAddressed(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{Owner: mine, Name: "do", Type: "DigitalOcean", Region: "sfo3"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	if status, body := send(t, app, http.MethodPut, "/v1/providers/"+mine+"/do",
		`{"provider":{"type":"DigitalOcean","region":"nyc3"}}`); status != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", status, body)
	}

	p := stored(t, mine, "do")
	if p == nil {
		t.Fatal("a body with no name blanked the row's primary key")
	}
	if p.Region != "nyc3" {
		t.Fatalf("region = %q, want the edit to have landed", p.Region)
	}
}

// TestProviderReplaceKeepsAMaskedSecret pins the property a provider surface
// lives or dies by: a read hands back "***", so saving what you read must not
// write three asterisks over the credential.
func TestProviderReplaceKeepsAMaskedSecret(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{Owner: mine, Name: "do", Type: "DigitalOcean", ClientSecret: "dop_v1_real", Region: "sfo3"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	// Read it the way the console does, then save exactly that back.
	_, read := send(t, app, http.MethodGet, "/v1/providers/"+mine+"/do", "")
	var got object.Provider
	if err := json.Unmarshal([]byte(read), &got); err != nil {
		t.Fatalf("decode %s: %v", read, err)
	}
	got.Region = "nyc3"
	edited, err := json.Marshal(map[string]any{"provider": got})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if status, body := send(t, app, http.MethodPut, "/v1/providers/"+mine+"/do", string(edited)); status != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", status, body)
	}

	p := stored(t, mine, "do")
	if p == nil || p.ClientSecret != "dop_v1_real" {
		t.Fatalf("saving a masked read overwrote the credential: %v", p)
	}
	if p.Region != "nyc3" {
		t.Fatalf("region = %q, want the edit to have landed", p.Region)
	}
}

// TestProviderReplaceKeepsAMaskedKeySecret is the same property one field over.
// A provider's ROTATION keys are credentials too and mask the same way, so a
// save of what was read has to restore each of them as well — otherwise adding a
// second cloud account to a provider makes every subsequent edit in the console
// overwrite that account's key with three asterisks, and the launch that cycles
// onto it fails to authenticate.
func TestProviderReplaceKeepsAMaskedKeySecret(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{
		Owner: mine, Name: "do", Type: "DigitalOcean", ClientSecret: "dop_v1_first", Region: "sfo3",
		Keys: []object.ProviderKey{{Name: "second", Secret: "dop_v1_second", Region: "nyc3"}},
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	_, read := send(t, app, http.MethodGet, "/v1/providers/"+mine+"/do", "")
	var got object.Provider
	if err := json.Unmarshal([]byte(read), &got); err != nil {
		t.Fatalf("decode %s: %v", read, err)
	}
	if len(got.Keys) != 1 || got.Keys[0].Secret != "***" {
		t.Fatalf("a read left a rotation key unmasked: %s", read)
	}
	got.Region = "nyc3"
	edited, err := json.Marshal(map[string]any{"provider": got})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if status, body := send(t, app, http.MethodPut, "/v1/providers/"+mine+"/do", string(edited)); status != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", status, body)
	}

	p := stored(t, mine, "do")
	if p == nil || len(p.Keys) != 1 {
		t.Fatalf("the rotation key did not survive the save: %v", p)
	}
	if p.Keys[0].Secret != "dop_v1_second" {
		t.Fatalf("saving a masked read overwrote a rotation key with %q", p.Keys[0].Secret)
	}
}

// TestProviderRemoveIs204 pins the void answer, and that the row is really gone.
// The old wire put "Affected"/"Unaffected" in a 200 body, which made a caller
// parse prose to learn a distinction none of them acted on.
func TestProviderRemoveIs204(t *testing.T) {
	app := providerWire(t)
	mine := org(t)

	if _, err := object.AddProvider(&object.Provider{Owner: mine, Name: "do", Type: "DigitalOcean"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	status, body := send(t, app, http.MethodDelete, "/v1/providers/"+mine+"/do", "")
	if status != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", status, body)
	}
	if body != "" {
		t.Fatalf("DELETE answered a body %q, want none", body)
	}
	if p := stored(t, mine, "do"); p != nil {
		t.Fatalf("the provider survived its delete: %v", p)
	}
}
