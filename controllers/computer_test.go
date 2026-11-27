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
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/service"
)

// fakeZT stands in for the fabric: it answers one HTTP request over a pipe,
// records what arrived, and never touches a controller, a router or the
// network. Every assertion below is about the bytes on that pipe and the bytes
// that came back — no live ZT call is made anywhere in this package.
type fakeZT struct {
	mu sync.Mutex

	err         error
	status      int
	contentType string
	out         []byte

	services []string
	path     string
	header   http.Header
	body     []byte
}

func (f *fakeZT) Dial(service string) (net.Conn, error) {
	f.mu.Lock()
	f.services = append(f.services, service)
	status, ct, out, err := f.status, f.contentType, f.out, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		req, err := http.ReadRequest(bufio.NewReader(remote))
		if err != nil {
			return
		}
		body, _ := io.ReadAll(req.Body)
		f.mu.Lock()
		f.path, f.header, f.body = req.URL.Path, req.Header.Clone(), body
		f.mu.Unlock()
		if ct == "" {
			ct = "application/json"
		}
		if status == 0 {
			status = http.StatusOK
		}
		res := &http.Response{
			StatusCode:    status,
			Status:        strconv.Itoa(status) + " upstream",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{"Content-Type": {ct}},
			Body:          io.NopCloser(bytes.NewReader(out)),
			ContentLength: int64(len(out)),
		}
		_ = res.Write(remote)
	}()
	return local, nil
}

func (f *fakeZT) seen() ([]string, string, http.Header, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.services...), f.path, f.header, append([]byte(nil), f.body...)
}

// computerWire registers the same typed op and raw writer the real router
// installs, on a bare app without the filter chain. The question here is what
// the op SENDS and ANSWERS, so an authz middleware would only obscure it.
func computerWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	app.Use(zip.H(RawComputerForMachine))
	zip.Post(app, "/v1/machines/:owner/:name/computer", Computer)
	return app
}

type computerAnswer struct {
	status      int
	contentType string
	body        []byte
}

func askComputer(t *testing.T, app *zip.App, path, bearer, header, body string) computerAnswer {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	if header != "" {
		req.Header.Set(computerApprovalHeader, header)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return computerAnswer{status: res.StatusCode, contentType: res.Header.Get("Content-Type"), body: b}
}

func installDialer(t *testing.T, d service.ZTDialer) {
	t.Helper()
	previous := service.CurrentZTDialer()
	service.RegisterZTDialer(d)
	t.Cleanup(func() { service.RegisterZTDialer(previous) })
}

// TestComputerTunnelsVerbatim is the contract Cloud depends on: a machine's
// computer surface answers with hanzod's status, Content-Type and BYTES. The
// body here is not JSON on purpose — re-encoding would turn pixels into a
// base64 field and break a screenshot.
func TestComputerTunnelsVerbatim(t *testing.T) {
	owner := org(t)
	storedMachine(t, owner, "mac-1")
	want := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0x7f}
	fake := &fakeZT{status: http.StatusTeapot, contentType: "image/png", out: want}
	installDialer(t, fake)

	body := `{"approval":true,"action":"screenshot","text":"top secret keystroke"}`
	got := askComputer(t, computerWire(t),
		"/v1/machines/"+owner+"/mac-1/computer", signer(t, "https://test.id")(owner), "", body)

	if got.status != http.StatusTeapot {
		t.Errorf("status = %d, want %d from upstream", got.status, http.StatusTeapot)
	}
	if got.contentType != "image/png" {
		t.Errorf("Content-Type = %q, want image/png verbatim", got.contentType)
	}
	if !bytes.Equal(got.body, want) {
		t.Errorf("body = %x, want %x verbatim", got.body, want)
	}

	services, path, _, forwarded := fake.seen()
	if len(services) != 1 || services[0] != "hanzod."+owner+".zt" {
		t.Errorf("dialed %v, want [hanzod.%s.zt]", services, owner)
	}
	if path != computerPath {
		t.Errorf("upstream path = %q, want %q", path, computerPath)
	}
	if string(forwarded) != body {
		t.Errorf("upstream body = %q, want the caller's bytes %q", forwarded, body)
	}
}

// TestComputerForwardsApproval checks BOTH halves of the evidence Cloud
// gathered for an act op. Neither is regenerated or dropped: hanzod sees the
// exact header value and the exact body the caller sent.
func TestComputerForwardsApproval(t *testing.T) {
	owner := org(t)
	storedMachine(t, owner, "mac-2")
	fake := &fakeZT{status: http.StatusOK, contentType: "application/json", out: []byte(`{"ok":true}`)}
	installDialer(t, fake)

	body := `{"approval":true,"action":"click","x":42}`
	askComputer(t, computerWire(t),
		"/v1/machines/"+owner+"/mac-2/computer", signer(t, "https://test.id")(owner), "signed-evidence-1", body)

	_, _, header, forwarded := fake.seen()
	if got := header.Get(computerApprovalHeader); got != "signed-evidence-1" {
		t.Errorf("%s = %q, want the caller's value verbatim", computerApprovalHeader, got)
	}
	if string(forwarded) != body || !strings.Contains(string(forwarded), `"approval":true`) {
		t.Errorf("approval field not forwarded verbatim: %s", forwarded)
	}
}

// TestComputerFailsClosedWithoutFabric covers both ways the fabric can be
// absent: no dialer registered at all, and a dialer whose service is not there.
// Neither may fall back to public DNS, and neither may leak the underlying
// error (which could carry an IAM or router detail).
func TestComputerFailsClosedWithoutFabric(t *testing.T) {
	owner := org(t)
	storedMachine(t, owner, "mac-3")
	installDialer(t, nil)

	got := askComputer(t, computerWire(t),
		"/v1/machines/"+owner+"/mac-3/computer", signer(t, "https://test.id")(owner), "", `{"approval":true}`)
	if got.status != http.StatusServiceUnavailable {
		t.Fatalf("no dialer: status = %d %s, want 503", got.status, got.body)
	}
	if !strings.Contains(string(got.body), "machine computer service unavailable") {
		t.Errorf("no dialer: body = %q, want a clear 503", got.body)
	}

	fake := &fakeZT{err: errors.New("SECRET_ROUTER_ADDRESS: dial failed")}
	installDialer(t, fake)
	got = askComputer(t, computerWire(t),
		"/v1/machines/"+owner+"/mac-3/computer", signer(t, "https://test.id")(owner), "", `{"approval":true}`)
	if got.status != http.StatusServiceUnavailable {
		t.Fatalf("missing service: status = %d %s, want 503", got.status, got.body)
	}
	if strings.Contains(string(got.body), "SECRET_ROUTER_ADDRESS") {
		t.Errorf("dial error leaked to the caller: %s", got.body)
	}
}

// TestComputerDeniesForeignOrg is the object-level guard the authz seam does
// not do: any authenticated brand user may reach the route, so the handler
// must refuse a caller whose org is not the one in the address.
func TestComputerDeniesForeignOrg(t *testing.T) {
	owner := org(t)
	storedMachine(t, owner, "mac-4")
	fake := &fakeZT{status: http.StatusOK, out: []byte(`{"ok":true}`)}
	installDialer(t, fake)

	mint := signer(t, "https://test.id")
	got := askComputer(t, computerWire(t),
		"/v1/machines/"+owner+"/mac-4/computer", mint("someone-else"), "", `{"approval":true}`)
	if got.status != http.StatusForbidden {
		t.Fatalf("foreign org: status = %d %s, want 403", got.status, got.body)
	}
	if services, _, _, _ := fake.seen(); len(services) != 0 {
		t.Errorf("foreign org dialed the fabric: %v", services)
	}
}

// TestComputerMissingMachineIs404 proves the machine is really looked up and
// a made-up name cannot borrow another machine's fabric service.
func TestComputerMissingMachineIs404(t *testing.T) {
	owner := org(t)
	fake := &fakeZT{status: http.StatusOK, out: []byte(`{"ok":true}`)}
	installDialer(t, fake)

	got := askComputer(t, computerWire(t),
		"/v1/machines/"+owner+"/no-such-machine/computer", signer(t, "https://test.id")(owner), "", `{"approval":true}`)
	if got.status != http.StatusNotFound {
		t.Fatalf("missing machine: status = %d %s, want 404", got.status, got.body)
	}
	if services, _, _, _ := fake.seen(); len(services) != 0 {
		t.Errorf("missing machine dialed the fabric: %v", services)
	}
}
