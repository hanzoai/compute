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

// WhitelabelConfig holds per-hostname branding configuration.
type WhitelabelConfig struct {
	AppName      string `json:"appName"`
	LogoUrl      string `json:"logoUrl"`
	FaviconUrl   string `json:"faviconUrl"`
	PrimaryColor string `json:"primaryColor"`
	SupportUrl   string `json:"supportUrl"`
	DocsUrl      string `json:"docsUrl"`
	OrgFilter    string `json:"orgFilter"`
}

var defaultWhitelabelConfig = WhitelabelConfig{
	AppName:      "Hanzo Visor",
	LogoUrl:      "https://cdn.hanzo.ai/img/hanzo-logo.svg",
	FaviconUrl:   "https://cdn.hanzo.ai/static/favicon.png",
	PrimaryColor: "#ffffff",
	SupportUrl:   "https://hanzo.ai/support",
	DocsUrl:      "https://docs.hanzo.ai",
	OrgFilter:    "",
}

var whitelabelConfigs = map[string]WhitelabelConfig{
	"visor.hanzo.ai": {
		AppName:      "Hanzo Visor",
		LogoUrl:      "https://cdn.hanzo.ai/img/hanzo-logo.svg",
		FaviconUrl:   "https://cdn.hanzo.ai/static/favicon.png",
		PrimaryColor: "#ffffff",
		SupportUrl:   "https://hanzo.ai/support",
		DocsUrl:      "https://docs.hanzo.ai",
		OrgFilter:    "",
	},
	"visor.lux.network": {
		AppName:      "Lux Visor",
		LogoUrl:      "https://lux.network/logo.svg",
		FaviconUrl:   "https://lux.network/favicon.ico",
		PrimaryColor: "#0066ff",
		SupportUrl:   "https://lux.network/support",
		DocsUrl:      "https://docs.lux.network",
		OrgFilter:    "lux",
	},
	"visor.zoo.ngo": {
		AppName:      "Zoo Visor",
		LogoUrl:      "https://zoo.ngo/logo.svg",
		FaviconUrl:   "https://zoo.ngo/favicon.ico",
		PrimaryColor: "#00cc66",
		SupportUrl:   "https://zoo.ngo/support",
		DocsUrl:      "https://docs.zoo.ngo",
		OrgFilter:    "zoo",
	},
	"visor.pars.network": {
		AppName:      "Pars Visor",
		LogoUrl:      "https://pars.network/logo.svg",
		FaviconUrl:   "https://pars.network/favicon.ico",
		PrimaryColor: "#ff6600",
		SupportUrl:   "https://pars.network/support",
		DocsUrl:      "https://docs.pars.network",
		OrgFilter:    "pars",
	},
}

// GetWhitelabelConfig returns the branding config for a given hostname.
// Falls back to the default (Hanzo) config if no match is found.
func GetWhitelabelConfig(host string) *WhitelabelConfig {
	// Strip port if present (e.g. "visor.hanzo.ai:443" -> "visor.hanzo.ai")
	for i := 0; i < len(host); i++ {
		if host[i] == ':' {
			host = host[:i]
			break
		}
	}

	if cfg, ok := whitelabelConfigs[host]; ok {
		return &cfg
	}

	cfg := defaultWhitelabelConfig
	return &cfg
}
