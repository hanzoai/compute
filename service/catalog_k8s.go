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

package service

// catalog_k8s.go supplements the DigitalOcean resale catalog with the EKS and
// GKE worker instance types a managed-Kubernetes cluster's seed pool is priced
// on. Those types are not DigitalOcean slugs, so SizeBySlug could not find them
// and HourlyCents refused the create fail-closed. The prices here are the
// upstream on-demand hourly cost marked up by the SAME resale margin the DO
// catalog uses (HanzoPrice), so one margin governs every SKU and there is no
// second price rule.
//
// This is resolved BEFORE the DO catalog and without a network call, so a
// cluster create prices deterministically. These sizes are intentionally not in
// the resale /v1/sizes listing (that catalog is the DigitalOcean droplet resale
// surface); they exist so a BYOC cluster on AWS or Google Cloud can be gated and
// billed at Hanzo's margin over the cloud's own price.

// awsOnDemandUSD is the AWS EC2 Linux on-demand hourly cost (us-east-1) for the
// instance types commonly chosen as EKS worker nodes.
var awsOnDemandUSD = map[string]float64{
	"t3.medium":   0.0416,
	"t3.large":    0.0832,
	"t3.xlarge":   0.1664,
	"t3.2xlarge":  0.3328,
	"m5.large":    0.096,
	"m5.xlarge":   0.192,
	"m5.2xlarge":  0.384,
	"m5.4xlarge":  0.768,
	"m6i.large":   0.096,
	"m6i.xlarge":  0.192,
	"m6i.2xlarge": 0.384,
	"c5.large":    0.085,
	"c5.xlarge":   0.17,
	"c5.2xlarge":  0.34,
	"c6i.large":   0.085,
	"c6i.xlarge":  0.17,
	"r5.large":    0.126,
	"r5.xlarge":   0.252,
	"r6i.large":   0.126,
	"r6i.xlarge":  0.252,
}

// gcpOnDemandUSD is the Google Compute Engine on-demand hourly cost (us-central1)
// for the machine types commonly chosen as GKE worker nodes.
var gcpOnDemandUSD = map[string]float64{
	"e2-medium":      0.033503,
	"e2-standard-2":  0.067006,
	"e2-standard-4":  0.134012,
	"e2-standard-8":  0.268024,
	"e2-standard-16": 0.536048,
	"n1-standard-1":  0.0475,
	"n1-standard-2":  0.095,
	"n1-standard-4":  0.19,
	"n1-standard-8":  0.38,
	"n2-standard-2":  0.097118,
	"n2-standard-4":  0.194236,
	"n2-standard-8":  0.388472,
	"n2d-standard-2": 0.084492,
	"n2d-standard-4": 0.168984,
	"c2-standard-4":  0.2088,
	"c2-standard-8":  0.4176,
}

// managedK8sSize resolves an EKS or GKE worker instance type to a resale
// SizeInfo, marking up the upstream on-demand cost by the standard catalog
// margin. ok is false for a type in neither table, so the caller falls through
// to the DigitalOcean catalog. Nodes are never GPU here (GPU pools are a
// separate SKU), so the base markup applies.
func managedK8sSize(slug string) (SizeInfo, bool) {
	cost, ok := awsOnDemandUSD[slug]
	if !ok {
		cost, ok = gcpOnDemandUSD[slug]
	}
	if !ok {
		return SizeInfo{}, false
	}
	return SizeInfo{
		Slug:        slug,
		Available:   true,
		Currency:    "USD",
		PriceHourly: HanzoPrice(cost, false),
	}, true
}
