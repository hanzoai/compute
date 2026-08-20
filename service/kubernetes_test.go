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
	"strings"
	"testing"
)

// fakeK8s is one cloud: it either answers with its clusters or fails.
type fakeK8s struct {
	provider string
	clusters []*KubernetesCluster
	err      error
	deleted  []string
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
	if c.Provider() != K8sDigitalOcean {
		t.Errorf("provider = %q, want %q", c.Provider(), K8sDigitalOcean)
	}
	// Cluster-level work has no cluster id; NewDOKSClient demands one.
	if _, err := NewDOKSClient("tok", ""); err == nil {
		t.Error("NewDOKSClient accepted an empty cluster id")
	}
}

// An unsupported cloud says so. It must not return a client that answers emptily
// — that is indistinguishable from a cloud with no clusters.
func TestAnUnsupportedCloudIsRefusedNotFaked(t *testing.T) {
	for _, p := range []string{K8sAWS, K8sAzure, K8sGCP, K8sLinked, "Nonsense"} {
		c, err := NewKubernetesClient(p, "id", "secret", "region")
		if err == nil {
			t.Errorf("%s: got a client with no implementation behind it", p)
		}
		if c != nil {
			t.Errorf("%s: returned a non-nil client alongside an error", p)
		}
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
