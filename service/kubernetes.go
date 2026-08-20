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
	"fmt"
	"sort"
	"strings"
	"sync"
)

// KubernetesClientInterface is the one Kubernetes expression Visor coordinates
// across k8s-native clouds, the same shape MachineClientInterface has beside it.
// A cloud gains clusters by implementing KubernetesCapable on the provider client
// NewMachineClient already builds — never by being named in a second registry.
type KubernetesClientInterface interface {
	// Provider names the cloud, matching the provider Type strings used by
	// NewMachineClient ("DigitalOcean", "AWS", ...).
	Provider() string
	ListClusters(ctx context.Context) ([]*KubernetesCluster, error)
	GetCluster(ctx context.Context, id string) (*KubernetesClusterDetail, error)
	CreateCluster(ctx context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error)
	DeleteCluster(ctx context.Context, id string) error
	NodeMachines(ctx context.Context) ([]*Machine, error)
}

// KubernetesCapable is a provider client that ALSO speaks Kubernetes. Not every
// cloud sells managed clusters, so this is an assertion on the one provider
// client, never a second registry of clouds — NewMachineClient is the registry,
// and a second switch beside it would be a second list of vendor names to drift.
// providerDigitalOcean is DigitalOcean's name as NewMachineClient spells it.
// Stated once so the k8s side cannot drift from the machine side.
const providerDigitalOcean = "DigitalOcean"

// VolumeCapable, VpcCapable and LoadBalancerCapable are the other nouns a
// provider client may speak. Same rule as KubernetesCapable: an assertion on the
// one client, never a second registry of clouds.
type VolumeCapable interface {
	Volumes() VolumeClientInterface
}

type VpcCapable interface {
	Vpcs() VpcClientInterface
}

type LoadBalancerCapable interface {
	LoadBalancers() LoadBalancerClientInterface
}

type KubernetesCapable interface {
	Kubernetes() KubernetesClientInterface
}

// kubernetesFor asks a provider client for its Kubernetes face. A cloud that has
// none is not an error: it is a machine cloud, and saying so is the answer.
func kubernetesFor(c MachineClientInterface) (KubernetesClientInterface, bool) {
	k, ok := c.(KubernetesCapable)
	if !ok {
		return nil, false
	}
	return k.Kubernetes(), true
}

// ProviderStatus is one configured cloud and whether it answered. Reported by
// KubernetesProviderStatus so an empty cluster list means "you have none" only
// when every backend is ok — a list that silently drops an unreachable cloud is
// the same three bytes as an empty estate.
type ProviderStatus struct {
	Provider string `json:"provider"`
	// Account tells two credentials on one cloud apart. Empty when there is only one.
	Account string `json:"account,omitempty"`
	OK      bool   `json:"ok"`
	Reason  string `json:"reason,omitempty"`
}

// cloudProvider is one platform-account cloud: its provider name and the credentials
// Visor holds for it.
type cloudProvider struct {
	provider string
	name     string
	keyID    string
	secret   string
	region   string
}

// Credential is one cloud account this deployment may spend on: which cloud, a
// name to tell several accounts of the same cloud apart, and the keys.
//
// Exported because the SOURCE of these lives in object (the Provider rows), and
// object imports service — so the source is registered inward rather than read
// outward. Same seam, same reason, as object.RegisterMembership.
type Credential struct {
	Provider string // "DigitalOcean", "AWS", "Hetzner", ... as NewMachineClient spells it
	Name     string // tells two accounts of one cloud apart; "" means the only one
	KeyID    string
	Secret   string
	Region   string
}

var (
	credentialsMu sync.RWMutex
	credentials   func() []Credential
)

// RegisterCredentials teaches this process which cloud accounts it may spend on.
// A deployment that can enumerate them (object, from its Provider rows) registers
// the reader; nothing here knows how they are stored.
func RegisterCredentials(f func() []Credential) {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	credentials = f
}

// cloudProviders lists every cloud account, from the registered source when there
// is one. Falling back to the single configured DigitalOcean token keeps a
// deployment that registers nothing working exactly as it did.
func cloudProviders() []cloudProvider {
	credentialsMu.RLock()
	f := credentials
	credentialsMu.RUnlock()

	if f != nil {
		if creds := f(); len(creds) > 0 {
			out := make([]cloudProvider, 0, len(creds))
			for _, c := range creds {
				out = append(out, cloudProvider{
					provider: c.Provider, name: c.Name,
					keyID: c.KeyID, secret: c.Secret, region: c.Region,
				})
			}
			return out
		}
	}
	if t := digitalOceanToken(); t != "" {
		return []cloudProvider{{provider: providerDigitalOcean, secret: t}}
	}
	return nil
}

// account pairs a client with the credential row it was built from. The client
// knows its cloud and nothing else — which of several accounts on that cloud it
// is, is the registry's business, so it rides here instead of on the interface.
type account struct {
	KubernetesClientInterface
	name string
}

// ref is how a caller names this account: "DigitalOcean" when it is the only one
// on that cloud, "DigitalOcean/prod" when it is not.
func (a account) ref() string {
	if a.name == "" {
		return a.Provider()
	}
	return a.Provider() + "/" + a.name
}

// kubernetesClients builds a client per configured cloud. Construction failures
// are returned beside the clients rather than aborting: one cloud with a bad
// credential must not hide the others.
func kubernetesClients() ([]KubernetesClientInterface, []ProviderStatus) {
	var clients []KubernetesClientInterface
	var status []ProviderStatus
	for _, b := range cloudProviders() {
		// ONE registry. A cloud reaches visor exactly once, through the same
		// factory the machine plane uses, so there is never a second way to DO.
		mc, err := NewMachineClient(Credential{Provider: b.provider, Name: b.name, KeyID: b.keyID, Secret: b.secret, Region: b.region})
		if err != nil {
			status = append(status, ProviderStatus{Provider: b.provider, Account: b.name, OK: false, Reason: err.Error()})
			continue
		}
		k, ok := kubernetesFor(mc)
		if !ok {
			// A machine cloud with no managed clusters. Not a failure, and not
			// listed as one — it simply contributes no clusters.
			continue
		}
		clients = append(clients, account{KubernetesClientInterface: k, name: b.name})
		status = append(status, ProviderStatus{Provider: b.provider, Account: b.name, OK: true})
	}
	return clients, status
}

// KubernetesConfigured reports whether any cloud is configured, so a caller can
// answer "not configured" distinctly from "configured and empty".
func KubernetesConfigured() bool { return len(cloudProviders()) > 0 }

// KubernetesProviderStatus reports every configured cloud and whether it answers
// right now, by making the cheapest real call each one has. This is the health
// half of the plane; the cluster list is the data half, and neither reports the
// other's business.
func KubernetesProviderStatus(ctx context.Context) []ProviderStatus {
	clients, status := kubernetesClients()
	byProvider := map[string]int{}
	for i, s := range status {
		byProvider[s.Provider] = i
	}
	for _, c := range clients {
		if _, err := c.ListClusters(ctx); err != nil {
			if i, ok := byProvider[c.Provider()]; ok {
				status[i] = ProviderStatus{Provider: c.Provider(), OK: false, Reason: err.Error()}
			}
		}
	}
	sort.Slice(status, func(i, j int) bool { return status[i].Provider < status[j].Provider })
	return status
}

// listClustersAcross gathers clusters from every backend, keeping what answered
// and naming what did not. err is non-nil only when NO backend answered, so one
// unreachable cloud costs its own rows and not the whole fleet.
func listClustersAcross(ctx context.Context) ([]*KubernetesCluster, []string, error) {
	clients, _ := kubernetesClients()
	if len(clients) == 0 {
		return nil, nil, fmt.Errorf("no cloud provider is configured")
	}
	return gather(ctx, clients)
}

// gather is the fold: what answered, what did not. err only when NOTHING did.
func gather(ctx context.Context, clients []KubernetesClientInterface) ([]*KubernetesCluster, []string, error) {
	var out []*KubernetesCluster
	var failed []string
	for _, c := range clients {
		cs, err := c.ListClusters(ctx)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", c.Provider(), err))
			continue
		}
		for _, k := range cs {
			if k.Provider == "" {
				k.Provider = c.Provider()
			}
			out = append(out, k)
		}
	}
	if len(failed) == len(clients) {
		return nil, failed, fmt.Errorf("every cloud provider failed: %s", strings.Join(failed, "; "))
	}
	return out, failed, nil
}

// findCluster locates a cluster by id across backends and returns the client that
// owns it. Ids are provider-scoped, so this asks each in turn; a miss everywhere
// is (nil, nil, nil) and the caller renders "not found".
func findCluster(ctx context.Context, id string) (KubernetesClientInterface, *KubernetesClusterDetail, error) {
	clients, _ := kubernetesClients()
	if len(clients) == 0 {
		return nil, nil, fmt.Errorf("no cloud provider is configured")
	}
	return locate(ctx, clients, id)
}

// locate asks each cloud in turn for id, because ids are provider-scoped.
func locate(ctx context.Context, clients []KubernetesClientInterface, id string) (KubernetesClientInterface, *KubernetesClusterDetail, error) {
	var failed []string
	for _, c := range clients {
		detail, err := c.GetCluster(ctx, id)
		if err != nil {
			if IsNotFound(err) {
				continue
			}
			failed = append(failed, fmt.Sprintf("%s: %v", c.Provider(), err))
			continue
		}
		if detail != nil {
			if detail.Provider == "" {
				detail.Provider = c.Provider()
			}
			return c, detail, nil
		}
	}
	if len(failed) == len(clients) {
		return nil, nil, fmt.Errorf("every cloud provider failed: %s", strings.Join(failed, "; "))
	}
	return nil, nil, nil
}

// backendFor picks the cloud a create lands on. With one configured it is that
// one; with several the spec must say, because guessing puts a customer's
// cluster on a cloud they did not choose.
func backendFor(provider string) (KubernetesClientInterface, error) {
	clients, _ := kubernetesClients()
	return pick(clients, provider)
}

// pick chooses the cloud a create lands on.
func pick(clients []KubernetesClientInterface, provider string) (KubernetesClientInterface, error) {
	switch {
	case len(clients) == 0:
		return nil, fmt.Errorf("no cloud provider is configured")
	case provider == "" && len(clients) == 1:
		return clients[0], nil
	case provider == "":
		return nil, fmt.Errorf("provider is required: configured accounts are %s", strings.Join(refs(clients), ", "))
	}
	// An exact "cloud/account" names one row; a bare cloud names it only while it
	// is the sole account there. Picking the first of several would put a
	// customer's cluster on whichever credential happened to sort first.
	var onCloud []KubernetesClientInterface
	for _, c := range clients {
		if a, ok := c.(account); ok && a.ref() == provider {
			return c, nil
		}
		if c.Provider() == provider {
			onCloud = append(onCloud, c)
		}
	}
	switch len(onCloud) {
	case 1:
		return onCloud[0], nil
	case 0:
		return nil, fmt.Errorf("cloud provider %s is not configured", provider)
	}
	return nil, fmt.Errorf("%s has %d accounts: name one of %s", provider, len(onCloud), strings.Join(refs(onCloud), ", "))
}

// refs names each client the way a caller must spell it.
func refs(clients []KubernetesClientInterface) []string {
	out := make([]string, 0, len(clients))
	for _, c := range clients {
		if a, ok := c.(account); ok {
			out = append(out, a.ref())
			continue
		}
		out = append(out, c.Provider())
	}
	sort.Strings(out)
	return out
}
