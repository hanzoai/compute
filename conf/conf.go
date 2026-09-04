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

// Package conf is compute's ONE configuration reader. It replaces Beego's global
// AppConfig with a small, dependency-light loader: conf/app.conf is parsed once
// (ini format), each value's ${VAR||default} form is expanded against the
// environment exactly as Beego did, and lookups fall through env → file →
// built-in default. A direct process env var named for the key still wins, so a
// deployment overrides any setting without touching the file.
package conf

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"gopkg.in/ini.v1"
)

var (
	loadOnce sync.Once
	values   map[string]string
)

// appConfCandidates are the paths tried for app.conf, in order — the binary
// runs from the repo root (conf/app.conf) while a package test runs from its
// own dir (../conf/app.conf). A miss is not fatal: env + defaults still serve.
var appConfCandidates = []string{
	"conf/app.conf",
	"../conf/app.conf",
	"../../conf/app.conf",
}

// envExpr matches Beego's ${VAR||default} (and bare ${VAR}) interpolation.
var envExpr = regexp.MustCompile(`\$\{([^}|]+)(?:\|\|([^}]*))?\}`)

func load() {
	values = map[string]string{}
	var path string
	for _, p := range appConfCandidates {
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return
	}
	cfg, err := ini.Load(path)
	if err != nil {
		return
	}
	for _, key := range cfg.Section("").KeyStrings() {
		values[key] = expand(strings.Trim(cfg.Section("").Key(key).String(), `"`))
	}
}

// expand resolves ${VAR||default} against the environment: VAR from the env, or
// the default when unset/empty. A raw value with no ${...} is returned as-is.
func expand(v string) string {
	return envExpr.ReplaceAllStringFunc(v, func(m string) string {
		g := envExpr.FindStringSubmatch(m)
		name, def := strings.TrimSpace(g[1]), g[2]
		if val, ok := os.LookupEnv(name); ok && val != "" {
			return val
		}
		return def
	})
}

func GetConfigString(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	loadOnce.Do(load)
	res := values[key]
	if res == "" {
		if key == "staticBaseUrl" {
			res = "https://cdn.hanzo.ai"
		} else if key == "logConfig" {
			res = "{\"filename\": \"logs/compute.log\", \"maxdays\":99999, \"perm\":\"0770\"}"
		}
	}

	return res
}

func GetConfigBool(key string) bool {
	value := GetConfigString(key)
	if value == "true" {
		return true
	} else {
		return false
	}
}

func GetConfigDataSourceName() string {
	dataSourceName := GetConfigString("dataSourceName")

	runningInDocker := os.Getenv("RUNNING_IN_DOCKER")
	if runningInDocker == "true" {
		// https://stackoverflow.com/questions/48546124/what-is-linux-equivalent-of-host-docker-internal
		if runtime.GOOS == "linux" {
			dataSourceName = strings.ReplaceAll(dataSourceName, "localhost", "172.17.0.1")
		} else {
			dataSourceName = strings.ReplaceAll(dataSourceName, "localhost", "host.docker.internal")
		}
	}

	return dataSourceName
}

func GetLanguage(language string) string {
	if language == "" || language == "*" {
		return "en"
	}

	if len(language) != 2 || language == "nu" {
		return "en"
	} else {
		return language
	}
}

// ConfPath returns the resolved app.conf path (or "" if none was found) — used
// by callers that log where config came from.
func ConfPath() string {
	for _, p := range appConfCandidates {
		if abs, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	return ""
}
