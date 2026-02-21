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

package autoscaler

// sizeSpec describes a DO droplet size.
type sizeSpec struct {
	Slug      string
	CPUMillis int64 // millicores
	MemMB     int64 // megabytes
}

// availableSizes lists DO K8s-eligible sizes, ordered by cost (ascending).
var availableSizes = []sizeSpec{
	{"s-1vcpu-2gb", 1000, 2048},
	{"s-2vcpu-2gb", 2000, 2048},
	{"s-2vcpu-4gb", 2000, 4096},
	{"s-4vcpu-8gb", 4000, 8192},
	{"s-8vcpu-16gb", 8000, 16384},
	{"s-16vcpu-32gb", 16000, 32768},
	{"s-32vcpu-64gb", 32000, 65536},
	// General purpose
	{"g-2vcpu-8gb", 2000, 8192},
	{"g-4vcpu-16gb", 4000, 16384},
	{"g-8vcpu-32gb", 8000, 32768},
	// CPU-optimized
	{"c-2vcpu-4gb", 2000, 4096},
	{"c-4vcpu-8gb", 4000, 8192},
	{"c-8vcpu-16gb", 8000, 16384},
	// Memory-optimized
	{"m-2vcpu-16gb", 2000, 16384},
	{"m-4vcpu-32gb", 4000, 32768},
}

// SizeForResources returns the best-fit DO size slug for given CPU (millicores) and memory (MB) requests.
// It picks the smallest standard size that can accommodate the request.
// Falls back to "s-4vcpu-8gb" as a safe default if nothing matches.
func SizeForResources(cpuMillis int64, memMB int64) string {
	// Filter to standard sizes first (cheapest), then try specialized
	standardSizes := []sizeSpec{
		{"s-1vcpu-2gb", 1000, 2048},
		{"s-2vcpu-2gb", 2000, 2048},
		{"s-2vcpu-4gb", 2000, 4096},
		{"s-4vcpu-8gb", 4000, 8192},
		{"s-8vcpu-16gb", 8000, 16384},
		{"s-16vcpu-32gb", 16000, 32768},
		{"s-32vcpu-64gb", 32000, 65536},
	}

	for _, s := range standardSizes {
		if cpuMillis <= s.CPUMillis && memMB <= s.MemMB {
			return s.Slug
		}
	}

	// If standard sizes don't fit, try all available sizes
	for _, s := range availableSizes {
		if cpuMillis <= s.CPUMillis && memMB <= s.MemMB {
			return s.Slug
		}
	}

	// Default to a large standard size
	return "s-4vcpu-8gb"
}

// SizeCPUMillis returns the CPU capacity in millicores for a given size slug.
func SizeCPUMillis(sizeSlug string) int64 {
	for _, s := range availableSizes {
		if s.Slug == sizeSlug {
			return s.CPUMillis
		}
	}
	return 0
}

// SizeMemMB returns the memory capacity in MB for a given size slug.
func SizeMemMB(sizeSlug string) int64 {
	for _, s := range availableSizes {
		if s.Slug == sizeSlug {
			return s.MemMB
		}
	}
	return 0
}
