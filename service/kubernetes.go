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
)

// KubernetesClientInterface is the one Kubernetes expression Visor coordinates
// across k8s-native clouds, the same shape MachineClientInterface has one plane
// down. A cloud is added by writing this interface once and naming it in
// NewKubernetesClient; nothing above here changes.
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

// Kubernetes provider names. These match the machine-plane provider Type strings
// so one provider row drives both planes.
const (
	K8sDigitalOcean = "DigitalOcean"
	K8sAWS          = "AWS"
	K8sAzure        = "Azure"
	K8sGCP          = "Google Cloud"
	K8sLinked       = "Linked" // k8s on linked Hanzo computers
)

// NewKubernetesClient builds the client for one cloud. Mirrors NewMachineClient:
// the switch IS the registry, so a cloud is present or it is not, and an
// unsupported one says so instead of returning something that answers emptily.
func NewKubernetesClient(providerType, accessKeyId, accessKeySecret, region string) (KubernetesClientInterface, error) {
	switch providerType {
	case K8sDigitalOcean:
		token := accessKeySecret
		if token == "" {
			token = accessKeyId
		}
		return newDOKSCloudClient(token)
	case K8sAWS, K8sAzure, K8sGCP, K8sLinked:
		return nil, fmt.Errorf("kubernetes provider %s: not implemented yet", providerType)
	default:
		return nil, fmt.Errorf("unsupported kubernetes provider type: %s", providerType)
	}
}

// ProviderStatus is one configured cloud and whether it answered. Reported by
// KubernetesProviderStatus so an empty cluster list means "you have none" only
// when every backend is ok — a list that silently drops an unreachable cloud is
// the same three bytes as an empty estate.
type ProviderStatus struct {
	Provider string `json:"provider"`
	OK       bool   `json:"ok"`
	Reason   string `json:"reason,omitempty"`
}

// cloudProvider is one platform-account cloud: its provider name and the credentials
// Visor holds for it.
type cloudProvider struct {
	provider string
	keyID    string
	secret   string
	region   string
}

// cloudProviders lists the clouds Visor has platform credentials for. Adding a cloud
// is one entry here plus its case in NewKubernetesClient.
func cloudProviders() []cloudProvider {
	var out []cloudProvider
	if t := digitalOceanToken(); t != "" {
		out = append(out, cloudProvider{provider: K8sDigitalOcean, secret: t})
	}
	return out
}

// kubernetesClients builds a client per configured cloud. Construction failures
// are returned beside the clients rather than aborting: one cloud with a bad
// credential must not hide the others.
func kubernetesClients() ([]KubernetesClientInterface, []ProviderStatus) {
	var clients []KubernetesClientInterface
	var status []ProviderStatus
	for _, b := range cloudProviders() {
		c, err := NewKubernetesClient(b.provider, b.keyID, b.secret, b.region)
		if err != nil {
			status = append(status, ProviderStatus{Provider: b.provider, OK: false, Reason: err.Error()})
			continue
		}
		clients = append(clients, c)
		status = append(status, ProviderStatus{Provider: b.provider, OK: true})
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
		names := make([]string, 0, len(clients))
		for _, c := range clients {
			names = append(names, c.Provider())
		}
		sort.Strings(names)
		return nil, fmt.Errorf("provider is required: configured backends are %s", strings.Join(names, ", "))
	}
	for _, c := range clients {
		if c.Provider() == provider {
			return c, nil
		}
	}
	return nil, fmt.Errorf("kubernetes provider %s is not configured", provider)
}
