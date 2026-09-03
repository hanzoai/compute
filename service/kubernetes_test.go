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

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeK8s is one cloud: it either answers with its clusters or fails.
type fakeK8s struct {
	provider string
	clusters []*KubernetesCluster
	err      error
	deleted  []string
	pooled   []string
	scaled   []string
	dropped  []string
}

func (f *fakeK8s) Provider() string { return f.provider }

func (f *fakeK8s) ListClusters(context.Context) ([]*KubernetesCluster, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.clusters, nil
}

func (f *fakeK8s) GetCluster(_ context.Context, id string) (*KubernetesClusterDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, c := range f.clusters {
		if c.ID == id {
			return &KubernetesClusterDetail{KubernetesCluster: *c}, nil
		}
	}
	return nil, nil
}

func (f *fakeK8s) CreateCluster(_ context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &KubernetesCluster{ID: "new-" + f.provider, Name: spec.Name, Tags: tags}, nil
}

func (f *fakeK8s) DeleteCluster(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeK8s) CreateNodePool(_ context.Context, clusterID string, spec *CreateNodePoolSpec) (*NodePool, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.pooled = append(f.pooled, clusterID+"/"+spec.Name)
	return &NodePool{ID: "p-" + spec.Name, Name: spec.Name, Size: spec.Size, Count: spec.Count}, nil
}

func (f *fakeK8s) ScaleNodePool(_ context.Context, clusterID, poolID string, count int) (*NodePool, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.scaled = append(f.scaled, fmt.Sprintf("%s/%s=%d", clusterID, poolID, count))
	return &NodePool{ID: poolID, Count: count}, nil
}

func (f *fakeK8s) DeleteNodePool(_ context.Context, clusterID, poolID string) error {
	if f.err != nil {
		return f.err
	}
	f.dropped = append(f.dropped, clusterID+"/"+poolID)
	return nil
}

func (f *fakeK8s) GetCredentials(_ context.Context, clusterID string) (*ClusterCredentials, error) {
	if f.err != nil {
		return nil, f.err
	}
	if _, ok := f.byID(clusterID); !ok {
		return nil, nil
	}
	return &ClusterCredentials{Endpoint: "https://" + clusterID + ".example", CAData: []byte("ca"), Token: "tok-" + clusterID}, nil
}

func (f *fakeK8s) byID(id string) (*KubernetesCluster, bool) {
	for _, c := range f.clusters {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

func (f *fakeK8s) NodeMachines(context.Context) ([]*Machine, error) { return nil, f.err }

var _ KubernetesClientInterface = (*fakeK8s)(nil)

// DOKS must satisfy the interface, or the one implemented cloud is not reachable
// through the dispatch that every other cloud will be added to.
func TestDOKSIsAKubernetesBackend(t *testing.T) {
	var _ KubernetesClientInterface = (*DOKSClient)(nil)
	c, err := newDOKSCloudClient("tok")
	if err != nil {
		t.Fatalf("newDOKSCloudClient: %v", err)
	}
	if c.Provider() != providerDigitalOcean {
		t.Errorf("provider = %q, want %q", c.Provider(), providerDigitalOcean)
	}
	// Cluster-level work has no cluster id; NewDOKSClient demands one.
	if _, err := NewDOKSClient("tok", ""); err == nil {
		t.Error("NewDOKSClient accepted an empty cluster id")
	}
}

// A cloud with no managed clusters contributes none, and is not an error. This
// replaces a test of the second registry that used to live here: there is no
// k8s-specific list of clouds to be absent from any more, only whether the ONE
// provider client speaks Kubernetes.
func TestAMachineOnlyCloudContributesNoClusters(t *testing.T) {
	machineOnly, err := NewMachineClient(Credential{Provider: "Hetzner", KeyID: "id", Secret: "secret", Region: "fsn1"})
	if err != nil {
		t.Skipf("hetzner client unavailable: %v", err)
	}
	if _, ok := kubernetesFor(machineOnly); ok {
		t.Error("Hetzner reported a Kubernetes face it does not have")
	}
	do, err := NewMachineClient(Credential{Provider: providerDigitalOcean, Secret: "tok"})
	if err != nil {
		t.Fatalf("digitalocean client: %v", err)
	}
	k, ok := kubernetesFor(do)
	if !ok {
		t.Fatal("DigitalOcean lost its Kubernetes face")
	}
	if k.Provider() != providerDigitalOcean {
		t.Errorf("provider = %q, want %q", k.Provider(), providerDigitalOcean)
	}
}

// One cloud failing costs its own rows, not the whole fleet. This is the shape of
// the 2026-08-15 outage: a dead provider reported as an empty estate.
func TestOneDeadCloudDoesNotHideTheOthers(t *testing.T) {
	live := &fakeK8s{provider: "Live", clusters: []*KubernetesCluster{{ID: "a", Name: "a"}}}
	dead := &fakeK8s{provider: "Dead", err: errors.New("401")}

	out, failed, err := gather(context.Background(), []KubernetesClientInterface{live, dead})
	if err != nil {
		t.Fatalf("expected the live cloud's rows, got error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("lost the live cloud's clusters: %v", out)
	}
	if len(failed) != 1 || !strings.Contains(failed[0], "Dead") {
		t.Fatalf("the dead cloud was not named: %v", failed)
	}
	if out[0].Provider != "Live" {
		t.Errorf("cluster not stamped with its cloud: %q", out[0].Provider)
	}
}

// Every cloud failing is an error, not an empty list. Silence here is the bug.
func TestEveryCloudFailingIsAnError(t *testing.T) {
	a := &fakeK8s{provider: "A", err: errors.New("401")}
	b := &fakeK8s{provider: "B", err: errors.New("timeout")}
	out, failed, err := gather(context.Background(), []KubernetesClientInterface{a, b})
	if err == nil {
		t.Fatal("all backends down reported as success")
	}
	if len(out) != 0 {
		t.Errorf("expected no clusters, got %v", out)
	}
	if len(failed) != 2 {
		t.Errorf("expected both named, got %v", failed)
	}
}

// With several clouds configured a create must say which one, because guessing
// puts a customer's cluster on a cloud they did not choose.
func TestCreateAcrossSeveralCloudsDemandsAProvider(t *testing.T) {
	clients := []KubernetesClientInterface{
		&fakeK8s{provider: "A"}, &fakeK8s{provider: "B"},
	}
	if _, err := pick(clients, ""); err == nil {
		t.Fatal("picked a cloud on the customer's behalf")
	} else if !strings.Contains(err.Error(), "A") || !strings.Contains(err.Error(), "B") {
		t.Errorf("refusal does not name the choices: %v", err)
	}
	got, err := pick(clients, "B")
	if err != nil || got.Provider() != "B" {
		t.Fatalf("naming a cloud did not select it: %v %v", got, err)
	}
	if _, err := pick(clients, "C"); err == nil {
		t.Error("accepted a cloud that is not configured")
	}
}

// One cloud configured needs no choice — the common case stays one call.
func TestOneCloudNeedsNoChoice(t *testing.T) {
	only := []KubernetesClientInterface{&fakeK8s{provider: "Only"}}
	got, err := pick(only, "")
	if err != nil || got.Provider() != "Only" {
		t.Fatalf("a single configured cloud should be implicit: %v %v", got, err)
	}
}

// A lookup asks each cloud in turn, because ids are provider-scoped.
func TestLookupSearchesEveryCloud(t *testing.T) {
	a := &fakeK8s{provider: "A", clusters: []*KubernetesCluster{{ID: "x"}}}
	b := &fakeK8s{provider: "B", clusters: []*KubernetesCluster{{ID: "y"}}}
	clients := []KubernetesClientInterface{a, b}

	owner, detail, err := locate(context.Background(), clients, "y")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if detail == nil || owner == nil || owner.Provider() != "B" {
		t.Fatalf("did not find y on its own cloud: owner=%v detail=%v", owner, detail)
	}
	if detail.Provider != "B" {
		t.Errorf("detail not stamped with its cloud: %q", detail.Provider)
	}
	_, missing, err := locate(context.Background(), clients, "nope")
	if err != nil || missing != nil {
		t.Errorf("an id no cloud has must be a clean miss, got %v %v", missing, err)
	}
}

// Two accounts on ONE cloud is the point: several DO keys, or DO beside AWS.
// A bare cloud name cannot pick between them, and picking the first would put a
// customer's cluster on whichever credential happened to sort first.
func TestTwoAccountsOnOneCloudMustBeNamed(t *testing.T) {
	prod := account{KubernetesClientInterface: &fakeK8s{provider: "DigitalOcean"}, name: "prod"}
	dev := account{KubernetesClientInterface: &fakeK8s{provider: "DigitalOcean"}, name: "dev"}
	clients := []KubernetesClientInterface{prod, dev}

	if _, err := pick(clients, "DigitalOcean"); err == nil {
		t.Fatal("a bare cloud name picked between two accounts on it")
	} else if !strings.Contains(err.Error(), "prod") || !strings.Contains(err.Error(), "dev") {
		t.Errorf("refusal does not name the accounts: %v", err)
	}
	got, err := pick(clients, "DigitalOcean/dev")
	if err != nil {
		t.Fatalf("naming an account did not select it: %v", err)
	}
	if a, ok := got.(account); !ok || a.name != "dev" {
		t.Errorf("selected the wrong account: %#v", got)
	}
}

// One account on a cloud still answers to the bare cloud name, so the common
// single-account case needs no ceremony.
func TestASoleAccountAnswersToItsCloudName(t *testing.T) {
	only := []KubernetesClientInterface{
		account{KubernetesClientInterface: &fakeK8s{provider: "Hetzner"}, name: "main"},
	}
	if _, err := pick(only, "Hetzner"); err != nil {
		t.Fatalf("sole account on a cloud should answer to the cloud: %v", err)
	}
}

// Credentials come from whoever registered them, so a deployment can hold many
// accounts across many clouds without this package knowing where they are kept.
func TestRegisteredCredentialsReplaceTheSingleToken(t *testing.T) {
	t.Cleanup(func() { RegisterCredentials(nil) })
	RegisterCredentials(func() []Credential {
		return []Credential{
			{Provider: "DigitalOcean", Name: "prod", Secret: "a"},
			{Provider: "DigitalOcean", Name: "dev", Secret: "b"},
			{Provider: "Hetzner", Name: "eu", Secret: "c"},
		}
	})
	got := cloudProviders()
	if len(got) != 3 {
		t.Fatalf("expected 3 accounts, got %d: %#v", len(got), got)
	}
	var dos int
	for _, p := range got {
		if p.provider == "DigitalOcean" {
			dos++
		}
	}
	if dos != 2 {
		t.Errorf("two DigitalOcean keys should both be accounts, got %d", dos)
	}
}

// ONE registry. NewMachineClient is the only place a cloud name is matched, and
// every other noun — volumes, vpcs, load balancers, kubernetes — is a capability
// of the client it returns.
//
// This is a gate, not a note. Four factories each carried their own list of
// vendor names, so adding a cloud to one and not the others was a silent drift
// waiting to happen. A second list reintroduced anywhere fails here.
func TestOnlyOneFileMatchesProviderNames(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "machine.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			code := line
			if c := strings.Index(code, "//"); c >= 0 {
				code = code[:c] // a comment may name a cloud; only code may not match on one
			}
			if strings.Contains(code, "providerType ==") {
				t.Errorf("%s:%d matches a provider name outside the registry: %s\n"+
					"    add a capability to the client NewMachineClient returns instead",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// An empty registration falls back to the platform account, which exists only
// where a carrier can reach it. There is no token to fall back to any more, so
// the two answers are "the platform account" and "nothing", and which one is
// correct is decided by the carrier alone.
func TestNoRegistrationFallsBackToThePlatformAccount(t *testing.T) {
	t.Cleanup(func() { RegisterCredentials(nil); RegisterCarrier(nil) })
	RegisterCredentials(func() []Credential { return nil })

	RegisterCarrier(nil)
	if got := cloudProviders(); len(got) != 0 {
		t.Errorf("no carrier and no registration should yield no accounts, got %d", len(got))
	}

	RegisterCarrier(func(Credential) (*http.Client, error) { return &http.Client{}, nil })
	got := cloudProviders()
	if len(got) != 1 || got[0].provider != providerDigitalOcean {
		t.Fatalf("a carrier should yield exactly the platform account, got %+v", got)
	}
	if got[0].secret != "" {
		t.Error("the platform account must carry no secret: egress attaches it")
	}
}

// Under a carrier, a cloud that cannot use it is REFUSED rather than falling back
// to holding the token itself. Silently bypassing the carrier is the one outcome
// worse than the cloud being unavailable: the credential is back in this process,
// which is the thing the carrier exists to prevent.
func TestACloudThatCannotBeCarriedIsRefused(t *testing.T) {
	t.Cleanup(func() { RegisterCarrier(nil) })
	RegisterCarrier(func(Credential) (*http.Client, error) { return &http.Client{}, nil })

	// Lightsail builds its own transport, so it cannot be carried yet.
	if _, err := NewMachineClient(Credential{Provider: "AWS Lightsail", KeyID: "k", Secret: "s", Region: "us-east-1"}); err == nil {
		t.Fatal("Lightsail was built under a carrier it cannot use — the token would be held here")
	} else if !strings.Contains(err.Error(), "credential directly") {
		t.Errorf("refusal does not say why: %v", err)
	}
	// These take our transport, so they are carried.
	for _, p := range []string{providerDigitalOcean, "Hetzner", providerAWS, providerGCP} {
		if _, err := NewMachineClient(Credential{Provider: p, Region: "us-east-1"}); err != nil {
			t.Errorf("%s should be carried, got: %v", p, err)
		}
	}
}

// Without a carrier the same cloud builds normally — a local or single-binary run
// holds its own tokens and is unaffected.
func TestWithoutACarrierEveryCloudStillBuilds(t *testing.T) {
	RegisterCarrier(nil)
	if _, err := NewMachineClient(Credential{Provider: "AWS", KeyID: "k", Secret: "s", Region: "us-east-1"}); err != nil {
		t.Errorf("AWS should build with no carrier registered: %v", err)
	}
}

// A carried client carries NO token: the whole point is that the key is not here.
func TestACarriedClientHoldsNoToken(t *testing.T) {
	t.Cleanup(func() { RegisterCarrier(nil) })
	var saw Credential
	RegisterCarrier(func(c Credential) (*http.Client, error) { saw = c; return &http.Client{}, nil })

	if _, err := NewMachineClient(Credential{Provider: providerDigitalOcean, Name: "prod"}); err != nil {
		t.Fatalf("carried build: %v", err)
	}
	if saw.Provider != providerDigitalOcean || saw.Name != "prod" {
		t.Errorf("the carrier was not told which account: %+v", saw)
	}
	if saw.Secret != "" {
		t.Errorf("a secret reached the carrier; under egress there is none to send")
	}
}

// A credential is minted only for a cluster carrying the caller's own hanzo-org
// tag. Another org's cluster — and one that is nowhere — both resolve to nothing,
// which the controller renders as "not found": guessing an id must never hand
// anyone a bearer token to somebody else's apiserver.
func TestCredentialsAreMintedOnlyForTheOwningOrg(t *testing.T) {
	cloud := &fakeK8s{provider: "Live", clusters: []*KubernetesCluster{
		{ID: "cl-acme", Name: "acme", Tags: []string{orgTag("acme")}},
		{ID: "cl-other", Name: "other", Tags: []string{orgTag("other")}},
	}}
	clients := []KubernetesClientInterface{cloud}
	ctx := context.Background()

	creds, err := mintCredentials(ctx, clients, "acme", "cl-acme")
	if err != nil {
		t.Fatalf("mintCredentials: %v", err)
	}
	if creds == nil || creds.Token != "tok-cl-acme" {
		t.Fatalf("the owning org got no credential: %+v", creds)
	}

	for _, id := range []string{"cl-other", "cl-nowhere"} {
		creds, err := mintCredentials(ctx, clients, "acme", id)
		if err != nil {
			t.Fatalf("mintCredentials(%s): %v", id, err)
		}
		if creds != nil {
			t.Fatalf("minted a credential for %s, which is not acme's: %+v", id, creds)
		}
	}
}
