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

// These tests pin the hosted machine routes to what hanzoai/cloud's compute
// client (apps/compute: client.go, compute.go, bots.go, types.go) sends and
// reads, request for request: the same method, path, ?owner, body fields and
// no-body DELETE, decoded the way that client decodes them — the
// {status,msg,data} envelope, the {machine, quote} launch answer, and the
// visorMachine field set. Behind the routes is the real EC2 SDK client against
// ec2test's fake account.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/service/commercetest"
	"github.com/hanzoai/compute/service/ec2test"
)

// hosted is the fake EC2 account for the whole test binary.
var hosted *ec2test.Fake

// startHosted points the hosted account at a fake for the whole binary, carries
// every cloud call through the stand-in egress in front of it, and returns its
// stop.
func startHosted() func() {
	f, stop := ec2test.New()
	for k, v := range f.Env() {
		_ = os.Setenv(k, v)
	}
	service.RegisterCarrier(func(c service.Credential) (*http.Client, error) { return f.Client(c.Provider, c.Name), nil })
	hosted = f
	return func() {
		service.RegisterCarrier(nil)
		stop()
	}
}

// hostedWire stands the hosted machine routes up as routers.Route registers them.
func hostedWire(t *testing.T) *zip.App {
	t.Helper()
	hosted.Reset()
	app := zip.New(zip.Config{ReadBufferSize: 16384})
	h := func(fn func(*ApiController)) zip.Handler {
		return func(c *zip.Ctx) error { fn(New(c)); return nil }
	}
	app.Get("/v1/sizes", h((*ApiController).GetComputeSizes))
	app.Get("/v1/regions", h((*ApiController).GetComputeRegions))
	app.Get("/v1/gpus", h((*ApiController).GetComputeGPUs))
	app.Get("/v1/machines", h((*ApiController).ListMachines))
	app.Post("/v1/machines", h((*ApiController).LaunchComputeMachine))
	zip.Get(app, "/v1/machines/agents", ListAgents)
	zip.Put(app, "/v1/machines/:owner/:name/agent", BindAgent)
	zip.Get(app, "/v1/machines/:owner/:name/agent", GetAgent)
	zip.Delete(app, "/v1/machines/:owner/:name/agent", UnbindAgent)
	app.Get("/v1/machines/:owner/:name", h((*ApiController).GetMachine))
	app.Put("/v1/machines/:owner/:name", h((*ApiController).UpdateMachine))
	app.Delete("/v1/machines/:owner/:name", h((*ApiController).DeleteMachine))
	return app
}

// cloudEnvelope is cloud's client.go envelope.
type cloudEnvelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// cloudMachine is cloud's types.go visorMachine, field for field.
type cloudMachine struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Id          string `json:"id"`
	Provider    string `json:"provider"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Region      string `json:"region"`
	Zone        string `json:"zone"`
	Type        string `json:"type"`
	Size        string `json:"size"`
	State       string `json:"state"`
	Image       string `json:"image"`
	Os          string `json:"os"`
	PublicIp    string `json:"publicIp"`
	PrivateIp   string `json:"privateIp"`
	CpuSize     string `json:"cpuSize"`
	MemSize     string `json:"memSize"`
	Tag         string `json:"tag"`
}

// cloudLaunch is cloud's compute.go launchReq, the POST /v1/machines body.
type cloudLaunch struct {
	Name         string `json:"name"`
	Size         string `json:"size"`
	InstanceType string `json:"instanceType"`
	Region       string `json:"region"`
	DryRun       bool   `json:"dryRun"`
	Kind         string `json:"kind"`
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
}

// call sends what cloud's cl.call sends and reads the envelope as it does.
func call(t *testing.T, app *zip.App, method, path string, body any) cloudEnvelope {
	t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	status, out := ask(t, app, method, path, "", raw)
	if status != http.StatusOK {
		t.Fatalf("%s %s = %d %s; the envelope routes answer 200", method, path, status, out)
	}
	var env cloudEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("%s %s: not an envelope: %s", method, path, out)
	}
	return env
}

// fundedCommerce is a commerce that can pay, counting reads and recording debits.
// Its balance moves as commerce's does: every debit comes off what is available,
// once per usage id.
type fundedCommerce struct {
	mu     sync.Mutex
	reads  int
	debits []commercetest.Usage
	seen   map[string]bool
}

func commerceWith(t *testing.T, availableCents int64) *fundedCommerce {
	t.Helper()
	c := &fundedCommerce{seen: map[string]bool{}}
	commercetest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/balance"):
			c.reads++
			_ = json.NewEncoder(w).Encode(map[string]any{"available": availableCents, "currency": "usd"})
		case strings.HasSuffix(r.URL.Path, "/usage"):
			u := commercetest.Read(r)
			c.debits = append(c.debits, u)
			if !c.seen[u.ID] {
				c.seen[u.ID] = true
				availableCents -= u.Cents()
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return c
}

// cloud's launchMachine: POST /v1/machines?owner=<org> with launchReq, dryRun
// first. The quote is Visor's, passed through verbatim; a real launch answers
// {machine, quote} and cloud renders the machine.
func TestCloudLaunchesAHostedMachine(t *testing.T) {
	app := hostedWire(t)
	money := commerceWith(t, 1_000_000)

	env := call(t, app, http.MethodPost, "/v1/machines?owner=acme",
		cloudLaunch{Name: "web-1", Size: "g5.xlarge", DryRun: true})
	if env.Status != "ok" {
		t.Fatalf("quote: %+v", env)
	}
	var quote LaunchQuote
	if err := json.Unmarshal(env.Data, &quote); err != nil {
		t.Fatal(err)
	}
	if quote.Org != "acme" || quote.Size != "g5.xlarge" || quote.Region != ec2test.Region ||
		quote.CentsHourly != 138 || quote.PriceHourly != 1.38 || quote.GPU == nil || quote.GPU.Model != "A10G" {
		t.Fatalf("quote = %+v", quote)
	}
	if len(hosted.Calls("")) != 0 || money.reads != 0 {
		t.Fatal("a quote reached the cloud or the balance")
	}

	env = call(t, app, http.MethodPost, "/v1/machines?owner=acme",
		cloudLaunch{Name: "web-1", Size: "g5.xlarge", Kind: "machine"})
	if env.Status != "ok" {
		t.Fatalf("launch: %+v", env)
	}
	var wrap struct {
		Machine cloudMachine `json:"machine"`
		Quote   LaunchQuote  `json:"quote"`
	}
	if err := json.Unmarshal(env.Data, &wrap); err != nil {
		t.Fatal(err)
	}
	m := wrap.Machine
	if m.Name == "" || m.Name != m.Id || !strings.HasPrefix(m.Id, "m-") {
		t.Fatalf("launched machine id %q name %q: cloud addresses it by name and binds it by id, so they are one", m.Id, m.Name)
	}
	if m.Owner != "acme" || m.DisplayName != "web-1" || m.Size != "g5.xlarge" || m.Region != ec2test.Region ||
		m.State != "Starting" || m.CpuSize != "4" || m.MemSize != "16384" {
		t.Fatalf("launched machine = %+v", m)
	}
	if runs := hosted.Calls("RunInstances"); len(runs) != 1 || runs[0].Form.Get("ClientToken") != m.Id {
		t.Fatalf("RunInstances = %+v", runs)
	}
	money.mu.Lock()
	defer money.mu.Unlock()
	if len(money.debits) != 1 || !strings.HasPrefix(money.debits[0].ID, "compute-"+m.Id+"-") ||
		money.debits[0].Cents() != 138 || money.debits[0].Org != "acme" {
		t.Fatalf("launch debits = %+v, want one 138-cent debit under the launch hour's meter id", money.debits)
	}
}

// cloud's managedMachines, getMachine, deleteMachine and a state write: GET the
// collection with ?owner, GET and DELETE the item at /v1/machines/{org}/{name}
// (DELETE with no body), and PUT the item with a state. Another org's machine
// is never in the answer and never acted on.
func TestCloudListsReadsStopsAndDeletesAHostedMachine(t *testing.T) {
	app := hostedWire(t)
	tags := func(org, id string) map[string]string {
		return map[string]string{"hanzo-org": org, "hanzo-machine": id, "managed-by": "hanzo-compute", "Name": id + "-name"}
	}
	mine := hosted.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: tags("acme", "m-cccccccccccccccccccc")})
	theirs := hosted.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: tags("beta", "m-dddddddddddddddddddd")})

	env := call(t, app, http.MethodGet, "/v1/machines?owner=acme", nil)
	var list []cloudMachine
	if err := json.Unmarshal(env.Data, &list); err != nil || env.Status != "ok" {
		t.Fatalf("list = %+v (%v)", env, err)
	}
	if len(list) != 1 || list[0].Name != "m-cccccccccccccccccccc" || list[0].Owner != "acme" ||
		list[0].PublicIp != "203.0.113.10" || list[0].CpuSize != "2" || list[0].State != "Running" {
		t.Fatalf("acme lists %+v", list)
	}

	env = call(t, app, http.MethodGet, "/v1/machines/acme/m-cccccccccccccccccccc", nil)
	var one cloudMachine
	if err := json.Unmarshal(env.Data, &one); err != nil || one.Name != "m-cccccccccccccccccccc" || one.DisplayName != "m-cccccccccccccccccccc-name" {
		t.Fatalf("get = %+v (%v)", env, err)
	}

	env = call(t, app, http.MethodPut, "/v1/machines/acme/m-cccccccccccccccccccc", map[string]string{"state": "Stopped"})
	if env.Status != "ok" || string(env.Data) != `"Affected"` {
		t.Fatalf("stop = %+v", env)
	}
	if stops := hosted.Calls("StopInstances"); len(stops) != 1 || stops[0].Form.Get("InstanceId.1") != mine.ID {
		t.Fatalf("stop reached %+v", stops)
	}

	// Another org's machine, named in the caller's own address: not found, so it
	// falls to the org's own rows, where there is nothing to drop.
	env = call(t, app, http.MethodDelete, "/v1/machines/acme/m-dddddddddddddddddddd", nil)
	if env.Status != "ok" || string(env.Data) != `"Unaffected"` {
		t.Fatalf("delete of another org's machine = %+v", env)
	}
	env = call(t, app, http.MethodDelete, "/v1/machines/acme/m-cccccccccccccccccccc", nil)
	if env.Status != "ok" || string(env.Data) != `"Affected"` {
		t.Fatalf("delete = %+v", env)
	}
	ends := hosted.Calls("TerminateInstances")
	if len(ends) != 1 || ends[0].Form.Get("InstanceId.1") != mine.ID {
		t.Fatalf("terminate reached %+v, want only %s (never %s)", ends, mine.ID, theirs.ID)
	}
}

// cloud's bindAgent after a bot launch: PUT /v1/machines/{org}/{id}/agent with
// {agentName, botVersion}, the id being the launched machine's. A hosted machine
// is what the binding resolves.
func TestCloudBindsAnAgentToAHostedMachine(t *testing.T) {
	app := hostedWire(t)
	id := "m-eeeeeeeeeeeeeeeeeeee"
	hosted.Add(ec2test.Instance{Type: "t3.medium", State: "running", Tags: map[string]string{
		"hanzo-org": "hostedbind", "hanzo-machine": id, "managed-by": "hanzo-compute", "hanzo-kind": "bot",
	}})

	status, body := ask(t, app, http.MethodPut, "/v1/machines/hostedbind/"+id+"/agent", "", `{"agentName":"bot-a","botVersion":""}`)
	if status != http.StatusOK {
		t.Fatalf("bind = %d %s", status, body)
	}
	var binding struct {
		Name      string `json:"name"`
		AgentName string `json:"agentName"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &binding); err != nil || binding.Name != id || binding.AgentName != "bot-a" {
		t.Fatalf("binding = %s (%v)", body, err)
	}
	if status, body := ask(t, app, http.MethodPut, "/v1/machines/hostedbind/m-ffffffffffffffffffff/agent", "", `{"agentName":"bot-a"}`); status == http.StatusOK {
		t.Fatalf("a binding to a machine the org does not have = %d %s", status, body)
	}
}

// cloud's regions and sizes: GET /v1/regions and /v1/sizes, passed through
// verbatim.
func TestCloudReadsTheCatalog(t *testing.T) {
	app := hostedWire(t)
	var sizes []map[string]any
	if env := call(t, app, http.MethodGet, "/v1/sizes", nil); json.Unmarshal(env.Data, &sizes) != nil || len(sizes) != 14 {
		t.Fatalf("sizes = %+v", env)
	}
	var regions []map[string]any
	if env := call(t, app, http.MethodGet, "/v1/regions", nil); json.Unmarshal(env.Data, &regions) != nil || len(regions) != 1 || regions[0]["slug"] != ec2test.Region {
		t.Fatalf("regions = %+v", env)
	}
	var gpus []map[string]any
	if env := call(t, app, http.MethodGet, "/v1/gpus", nil); json.Unmarshal(env.Data, &gpus) != nil || len(gpus) != 8 {
		t.Fatalf("gpus = %+v", env)
	}
}

// A launch the account cannot make is refused by name before the balance is
// read. A launch the account's quota does not yet cover — every GPU size, until
// AWS grants one — is refused saying so, and charges nothing.
func TestALaunchTheAccountCannotMakeChargesNothing(t *testing.T) {
	app := hostedWire(t)
	money := commerceWith(t, 1_000_000)

	t.Setenv("computeSubnet", "")
	env := call(t, app, http.MethodPost, "/v1/machines?owner=acme", cloudLaunch{Name: "web-1", Size: "t3.medium"})
	if env.Status != "error" || !strings.Contains(env.Msg, "computeSubnet") {
		t.Fatalf("unconfigured launch = %+v", env)
	}
	if money.reads != 0 || len(hosted.Calls("")) != 0 {
		t.Fatal("a launch refused on configuration asked commerce or the cloud")
	}
	t.Setenv("computeSubnet", ec2test.Subnet)

	service.RegisterCarrier(nil)
	env = call(t, app, http.MethodPost, "/v1/machines?owner=acme", cloudLaunch{Name: "web-1", Size: "t3.medium"})
	service.RegisterCarrier(func(c service.Credential) (*http.Client, error) { return hosted.Client(c.Provider, c.Name), nil })
	if env.Status != "error" || !strings.Contains(env.Msg, "hosted compute needs egress") {
		t.Fatalf("launch with no egress = %+v", env)
	}
	if money.reads != 0 || len(hosted.Calls("")) != 0 {
		t.Fatal("a launch with no egress asked commerce or the cloud")
	}

	env = call(t, app, http.MethodPost, "/v1/machines?owner=acme", cloudLaunch{Name: "web-1", Size: "t3.medium", Region: "sfo3"})
	if env.Status != "error" || !strings.Contains(env.Msg, "region sfo3 is not offered") {
		t.Fatalf("other-region launch = %+v", env)
	}

	hosted.Refuse("RunInstances", "VcpuLimitExceeded")
	env = call(t, app, http.MethodPost, "/v1/machines?owner=acme", cloudLaunch{Name: "trainer", Size: "p4d.24xlarge"})
	if env.Status != "error" || !strings.Contains(env.Msg, "p4d.24xlarge in us-east-1 cannot launch yet") || !strings.Contains(env.Msg, "quota") {
		t.Fatalf("quota refusal = %+v", env)
	}
	money.mu.Lock()
	defer money.mu.Unlock()
	if len(money.debits) != 0 {
		t.Fatalf("a refused launch was charged: %+v", money.debits)
	}
}
