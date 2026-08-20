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

// images_test.go pins the one property the image catalog has to have: an account
// with no images and a credential that stopped working must not produce the same
// answer. An empty list is a legitimate value, so it cannot also be the way a
// failure is reported.
package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// imagesStub serves the three catalog endpoints, each with its own status, so a
// test can fail one source and leave the others answering. Routes mirror the DO
// API paths godo calls: type=distribution / type=application, and bare /v2/images
// for the account's own uploads.
type imagesStub struct {
	distribution, application, user int // HTTP status per source; 0 means 200
	rows                            map[string][]godo.Image
}

func (s imagesStub) client(t *testing.T) *godo.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/images", func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("type")
		if kind == "" {
			kind = "user"
		}
		status := map[string]int{
			"distribution": s.distribution,
			"application":  s.application,
			"user":         s.user,
		}[kind]
		if status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "unauthorized", "message": "Unable to authenticate you"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"images": s.rows[kind]})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	gc := godo.NewClient(nil)
	u, _ := url.Parse(ts.URL + "/")
	gc.BaseURL = u
	return gc
}

// TestARevokedCredentialIsNotAnEmptyCatalog is the mechanism finding on this
// surface. Every read 401s, which is exactly what a revoked token does, and the
// old code answered with an empty slice and a nil error — "Hanzo offers no
// operating systems", served as a success.
func TestARevokedCredentialIsNotAnEmptyCatalog(t *testing.T) {
	cli := imagesStub{
		distribution: http.StatusUnauthorized,
		application:  http.StatusUnauthorized,
		user:         http.StatusUnauthorized,
	}.client(t)

	out, err := listImages(context.Background(), cli, "acme")
	if err == nil {
		t.Fatal("a catalog whose every read was refused reported success — an empty list is a legitimate answer, " +
			"so it can never also be how a failure is reported")
	}
	if len(out) != 0 {
		t.Fatalf("no source answered, so no rows can be honest; got %d", len(out))
	}
	for _, want := range []string{"distributions", "applications", "custom images"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure must name the sources that did not answer; %q omits %q", err, want)
		}
	}
}

// TestAnEmptyAccountIsNotAFailure is the other half of the distinction. Three
// sources answer, all with nothing — that IS the truth, and it must not be dressed
// up as an error.
func TestAnEmptyAccountIsNotAFailure(t *testing.T) {
	cli := imagesStub{}.client(t)

	out, err := listImages(context.Background(), cli, "acme")
	if err != nil {
		t.Fatalf("an account that answered with no images is not unreachable: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want an empty catalog, got %d rows", len(out))
	}
}

// TestAPartialCatalogKeepsTheRowsThatAnswered proves the degraded read is useful
// rather than merely loud: the sources that worked still come back, and the one
// that did not is named. A caller choosing what to launch sees the real
// distributions and is told their own uploads are missing, instead of being shown
// a short list that reads as complete.
func TestAPartialCatalogKeepsTheRowsThatAnswered(t *testing.T) {
	cli := imagesStub{
		user: http.StatusUnauthorized,
		rows: map[string][]godo.Image{
			"distribution": {{ID: 1, Slug: "ubuntu-24-04-x64", Name: "24.04 (LTS) x64", Distribution: "Ubuntu"}},
			"application":  {{ID: 2, Slug: "docker-20-04", Name: "Docker"}},
		},
	}.client(t)

	out, err := listImages(context.Background(), cli, "acme")
	if err == nil {
		t.Fatal("the custom-image read was refused, so the catalog is incomplete and must say so")
	}
	if !strings.Contains(err.Error(), "custom images") {
		t.Fatalf("the gap must be named precisely; got %q", err)
	}
	for _, unwanted := range []string{"distributions", "applications"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Fatalf("%q answered and must not be reported as missing; got %q", unwanted, err)
		}
	}
	if len(out) != 2 {
		t.Fatalf("the sources that answered must survive the failure of one that did not; got %d rows", len(out))
	}
}

// TestOnlyTheCallersOwnCustomImagesSurvive keeps the tenant filter honest while
// the surrounding error handling changes around it: another org's upload is not
// this org's to see, and a fold that now carries failures must not start carrying
// rows too.
func TestOnlyTheCallersOwnCustomImagesSurvive(t *testing.T) {
	cli := imagesStub{
		rows: map[string][]godo.Image{
			"user": {
				{ID: 10, Name: "acme-base", Tags: []string{orgTag("acme")}},
				{ID: 11, Name: "other-base", Tags: []string{orgTag("other")}},
			},
		},
	}.client(t)

	out, err := listImages(context.Background(), cli, "acme")
	if err != nil {
		t.Fatalf("every source answered: %v", err)
	}
	if len(out) != 1 || out[0].Name != "acme-base" {
		t.Fatalf("one org must never see another's uploads; got %+v", out)
	}
}
