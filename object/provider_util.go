// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package object

// isActiveCloudProvider reports whether a provider is an active cloud provider
// usable for machine and node-pool reconciliation. The API token may live in
// either ClientId or ClientSecret, and the category may be the generic "Cloud"
// as well as the legacy "Public Cloud"/"Private Cloud" labels.
func isActiveCloudProvider(p *Provider) bool {
	hasToken := p.ClientId != "" || p.ClientSecret != ""
	isCloud := p.Category == "Cloud" || p.Category == "Public Cloud" || p.Category == "Private Cloud"
	return hasToken && isCloud && p.State == "Active"
}

func getActiveCloudProviders(owner string) ([]*Provider, error) {
	providers, err := GetProviders(owner)
	if err != nil {
		return nil, err
	}

	res := []*Provider{}
	for _, provider := range providers {
		if isActiveCloudProvider(provider) {
			res = append(res, provider)
		}
	}
	return res, nil
}

// isActiveCloudProvider is the ONE predicate for "a usable BYOC cloud account":
// active, with both credential halves, in a cloud category. Shared by the per-owner
// and the cross-tenant accessors so both agree on what counts.
func isActiveCloudProvider(p *Provider) bool {
	return p.ClientId != "" && p.ClientSecret != "" && p.State == "Active" &&
		(p.Category == "Public Cloud" || p.Category == "Private Cloud")
}

// GetAllActiveCloudProviders returns every org's active BYOC cloud provider — the
// set the daily fleet cost collector bills 1% of spend against. Like the running-
// machine sweep it scans cross-tenant and each row carries its own Owner/Project, so
// one pass attributes every org's cloud fee correctly.
func GetAllActiveCloudProviders() ([]*Provider, error) {
	providers := []*Provider{}
	if err := adapter.engine.Where("state = ?", "Active").Find(&providers); err != nil {
		return nil, err
	}
	res := []*Provider{}
	for _, p := range providers {
		if isActiveCloudProvider(p) {
			res = append(res, p)
		}
	}
	return res, nil
}

func getActiveBlockchainProvider(owner string) (*Provider, error) {
	providers, err := GetProviders(owner)
	if err != nil {
		return nil, err
	}

	for _, provider := range providers {
		if provider.ClientId != "" && provider.ClientSecret != "" && provider.Category == "Blockchain" && provider.State == "Active" {
			return provider, nil
		}
	}
	return nil, nil
}
