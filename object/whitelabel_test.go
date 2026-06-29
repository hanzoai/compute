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

import "testing"

func TestGetWhitelabelConfig_KnownHosts(t *testing.T) {
	tests := []struct {
		host      string
		wantApp   string
		wantOrg   string
		wantColor string
	}{
		{"visor.hanzo.ai", "Hanzo Visor", "", "#ffffff"},
		{"visor.lux.network", "Lux Visor", "lux", "#0066ff"},
		{"visor.zoo.ngo", "Zoo Visor", "zoo", "#00cc66"},
		{"visor.pars.network", "Pars Visor", "pars", "#ff6600"},
	}

	for _, tt := range tests {
		cfg := GetWhitelabelConfig(tt.host)
		if cfg.AppName != tt.wantApp {
			t.Errorf("GetWhitelabelConfig(%q).AppName = %q, want %q", tt.host, cfg.AppName, tt.wantApp)
		}
		if cfg.OrgFilter != tt.wantOrg {
			t.Errorf("GetWhitelabelConfig(%q).OrgFilter = %q, want %q", tt.host, cfg.OrgFilter, tt.wantOrg)
		}
		if cfg.PrimaryColor != tt.wantColor {
			t.Errorf("GetWhitelabelConfig(%q).PrimaryColor = %q, want %q", tt.host, cfg.PrimaryColor, tt.wantColor)
		}
	}
}

func TestGetWhitelabelConfig_StripsPort(t *testing.T) {
	cfg := GetWhitelabelConfig("visor.lux.network:443")
	if cfg.AppName != "Lux Visor" {
		t.Errorf("expected Lux Visor with port suffix, got %q", cfg.AppName)
	}
}

func TestGetWhitelabelConfig_DefaultFallback(t *testing.T) {
	cfg := GetWhitelabelConfig("unknown.example.com")
	if cfg.AppName != "Hanzo Visor" {
		t.Errorf("expected default Hanzo Visor for unknown host, got %q", cfg.AppName)
	}
	if cfg.OrgFilter != "" {
		t.Errorf("expected empty OrgFilter for default, got %q", cfg.OrgFilter)
	}
}

func TestGetWhitelabelConfig_Localhost(t *testing.T) {
	cfg := GetWhitelabelConfig("localhost:19000")
	if cfg.AppName != "Hanzo Visor" {
		t.Errorf("expected default Hanzo Visor for localhost, got %q", cfg.AppName)
	}
}

func TestGetWhitelabelConfig_AllFieldsPopulated(t *testing.T) {
	for host, expected := range whitelabelConfigs {
		cfg := GetWhitelabelConfig(host)
		if cfg.AppName == "" {
			t.Errorf("GetWhitelabelConfig(%q).AppName is empty", host)
		}
		if cfg.LogoUrl == "" {
			t.Errorf("GetWhitelabelConfig(%q).LogoUrl is empty", host)
		}
		if cfg.FaviconUrl == "" {
			t.Errorf("GetWhitelabelConfig(%q).FaviconUrl is empty", host)
		}
		if cfg.PrimaryColor == "" {
			t.Errorf("GetWhitelabelConfig(%q).PrimaryColor is empty", host)
		}
		if cfg.SupportUrl == "" {
			t.Errorf("GetWhitelabelConfig(%q).SupportUrl is empty", host)
		}
		if cfg.DocsUrl == "" {
			t.Errorf("GetWhitelabelConfig(%q).DocsUrl is empty", host)
		}
		if cfg.AppName != expected.AppName {
			t.Errorf("GetWhitelabelConfig(%q).AppName = %q, want %q", host, cfg.AppName, expected.AppName)
		}
	}
}
