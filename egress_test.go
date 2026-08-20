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

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/egress/spend"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/service"
)

// door is a stand-in for egress on the ZAP address visor dials. It records what
// it was asked for and answers with what a cloud would have said.
type door struct {
	asked  atomic.Value // spend.Fetch
	bearer atomic.Value // string
	answer json.RawMessage
	status int
}

func (d *door) listen(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	app := zip.New(zip.Config{AppName: "egress"})
	app.Post("/v1/fetch", func(c *zip.Ctx) error {
		var in spend.Fetch
		if err := json.Unmarshal(c.Body(), &in); err != nil {
			return zip.ErrBadRequest("not a fetch")
		}
		d.asked.Store(in)
		d.bearer.Store(c.Header("Authorization"))
		return c.JSON(http.StatusOK, spend.Fetched{Status: d.status, Body: d.answer, Scope: "org"})
	})
	go func() { _ = app.Listen(addr) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	for i := 0; i < 200; i++ {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("egress stand-in never bound %s", addr)
	return ""
}

func (d *door) fetch(t *testing.T) spend.Fetch {
	t.Helper()
	in, ok := d.asked.Load().(spend.Fetch)
	if !ok {
		t.Fatal("egress was never called — the carrier is not on the path")
	}
	return in
}

// The whole point, end to end: a real DigitalOcean SDK client, built by the one
// provider registry, makes its calls through egress. Visor holds no cloud
// credential — the Credential here carries no secret at all — and the call still
// reaches the cloud, because the key is attached at egress.
func TestTheSDKCallsThroughEgress(t *testing.T) {
	d := &door{status: 200, answer: json.RawMessage(`{"droplets":[{"id":7,"name":"web-1","vcpus":2,"memory":4096,"status":"active","region":{"slug":"nyc3"},"size":{"slug":"s-2vcpu-4gb"},"image":{"slug":"ubuntu-24-04","distribution":"Ubuntu","name":"24.04"}}]}`)}
	addr := d.listen(t)

	t.Cleanup(func() { service.RegisterCarrier(nil) })
	service.RegisterCarrier(func(c service.Credential) (*http.Client, error) {
		return spend.Client(spend.Config{
			Network: "tcp", Address: addr, Token: "visor-own-token",
			Provider: c.Provider, Account: c.Name,
		}), nil
	})

	// No Secret. Under a carrier there is nothing to put there, which is the
	// property being tested: a pod with this configuration holds nothing that
	// spends.
	client, err := service.NewMachineClient(service.Credential{
		Provider: "DigitalOcean", Name: "prod", Region: "nyc3",
	})
	if err != nil {
		t.Fatalf("the registry refused a carried DigitalOcean: %v", err)
	}

	machines, err := client.GetMachines()
	if err != nil {
		t.Fatalf("the SDK call did not complete: %v", err)
	}

	asked := d.fetch(t)
	if asked.Provider != "DigitalOcean" {
		t.Errorf("egress was asked for provider %q", asked.Provider)
	}
	if asked.Label != "prod" {
		t.Errorf("egress was asked for account %q — the wrong credential would be spent", asked.Label)
	}
	if asked.Method != "GET" {
		t.Errorf("method = %q", asked.Method)
	}
	if got := d.bearer.Load(); got != "Bearer visor-own-token" {
		t.Errorf("egress got Authorization %v — visor did not identify itself", got)
	}
	if len(machines) != 1 {
		t.Fatalf("the SDK could not read the cloud's answer: %+v", machines)
	}
	if m := machines[0]; m.DisplayName != "web-1" || m.Region != "nyc3" || m.Size != "s-2vcpu-4gb" || m.State != "Running" {
		t.Errorf("the cloud's answer arrived damaged: %+v", m)
	}
}

// dial is one configuration value covering a socket on this host and a service
// across the network, so an operator writes an address and not a pair.
func TestDial(t *testing.T) {
	for address, want := range map[string][2]string{
		"egress.hanzo.ai:9653":          {"tcp", "egress.hanzo.ai:9653"},
		"tcp://egress.hanzo.ai:9653":    {"tcp", "egress.hanzo.ai:9653"},
		"unix:///run/hanzo/egress.sock": {"unix", "/run/hanzo/egress.sock"},
		"unix://run/hanzo/egress.sock":  {"unix", "run/hanzo/egress.sock"},
		"127.0.0.1:9653":                {"tcp", "127.0.0.1:9653"},
	} {
		network, addr := dial(address)
		if network != want[0] || addr != want[1] {
			t.Errorf("dial(%q) = %q, %q; want %q, %q", address, network, addr, want[0], want[1])
		}
	}
}

// carry() is the operator contract, and each of its three states matters.
func TestCarry(t *testing.T) {
	t.Run("unset, visor calls clouds itself", func(t *testing.T) {
		t.Setenv("egressAddress", "")
		t.Setenv("egressToken", "")
		t.Cleanup(func() { service.RegisterCarrier(nil) })
		service.RegisterCarrier(nil)

		if err := carry(); err != nil {
			t.Fatalf("an unconfigured visor must start: %v", err)
		}
		// AWS builds its own transport, so it is refused ONLY under a carrier.
		// Building here is what proves none was registered.
		if _, err := service.NewMachineClient(service.Credential{
			Provider: "AWS", KeyID: "k", Secret: "s", Region: "us-east-1",
		}); err != nil {
			t.Errorf("a carrier was registered when none was configured: %v", err)
		}
	})

	t.Run("an address with no token refuses to start", func(t *testing.T) {
		t.Setenv("egressAddress", "egress.hanzo.ai:9653")
		t.Setenv("egressToken", "")
		t.Cleanup(func() { service.RegisterCarrier(nil) })

		err := carry()
		if err == nil {
			t.Fatal("visor started with an address and no token — every cloud call would be a 401")
		}
		if !strings.Contains(err.Error(), "egressToken") {
			t.Errorf("the refusal does not name what is missing: %v", err)
		}
	})

	t.Run("both, and the cloud keys are egress's", func(t *testing.T) {
		t.Setenv("egressAddress", "tcp://egress.hanzo.ai:9653")
		t.Setenv("egressToken", "visor-own-token")
		t.Cleanup(func() { service.RegisterCarrier(nil) })
		service.RegisterCarrier(nil)

		if err := carry(); err != nil {
			t.Fatalf("carry: %v", err)
		}
		// Under a carrier, a cloud that cannot use one is refused rather than
		// falling back to holding a key.
		if _, err := service.NewMachineClient(service.Credential{
			Provider: "AWS", KeyID: "k", Secret: "s", Region: "us-east-1",
		}); err == nil {
			t.Error("AWS was built under a carrier it cannot use — the token would be held here")
		}
	})
}
