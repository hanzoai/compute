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

// Package kms seals provider secrets in Hanzo KMS and resolves them back.
//
// The model is a server-side org CEK: the vault is unlocked once at launch with
// a service secret read from the environment, and from then on compute seals and
// unseals unattended — no per-member key wrapping, because compute must resolve a
// launch credential with no human present. Encryption and decryption happen
// client-side (github.com/hanzoai/kms/sdk/go), so the MPC nodes hold only
// ciphertext.
//
// A sealed secret is persisted as a Ref (a flat key), never the plaintext. The
// SDK's Set returns no version, so a Ref carries none under this backend; the
// field is kept for a backend that does.
package kms

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/hanzoai/kms/sdk/go"
)

// ErrNotConfigured reports that no vault is wired — the process has no KMS env,
// so secrets stay in the plaintext column exactly as before KMS.
var ErrNotConfigured = errors.New("kms: not configured")

// Vault is the sealing surface compute depends on. It mirrors what the Hanzo KMS
// SDK exposes over a client-side org CEK (Set/Get on a flat key), so the real
// *sdk.Client satisfies it directly and a test substitutes a fake.
type Vault interface {
	Set(key string, value []byte) error
	Get(key string) ([]byte, error)
}

// refPrefix marks a stored value as a reference rather than a plaintext secret.
// It is the one thing that distinguishes a sealed row from a legacy plaintext
// row, which is what lets a read resolve either without a schema column.
const refPrefix = "kms:"

// Ref points at a sealed secret: the flat KMS key it lives under, and the
// version the backend returned when it surfaces one (empty for the org-CEK
// backend). It is what a provider row holds in place of the plaintext.
type Ref struct {
	Path    string
	Version string
}

// Encode renders a Ref as the string persisted in the row.
func (r Ref) Encode() string {
	if r.Version == "" {
		return refPrefix + r.Path
	}
	return refPrefix + r.Path + "#" + r.Version
}

// IsRef reports whether a stored value is a reference (vs. a legacy plaintext).
func IsRef(s string) bool { return strings.HasPrefix(s, refPrefix) }

// Decode parses a stored value into a Ref. ok is false for a plaintext value,
// which the caller then uses as-is (dual-read).
func Decode(s string) (Ref, bool) {
	if !IsRef(s) {
		return Ref{}, false
	}
	body := strings.TrimPrefix(s, refPrefix)
	if i := strings.LastIndex(body, "#"); i >= 0 {
		return Ref{Path: body[:i], Version: body[i+1:]}, true
	}
	return Ref{Path: body}, true
}

var (
	mu    sync.RWMutex
	vault Vault

	cacheMu sync.Mutex
	cache   = map[string]cacheEntry{}
)

type cacheEntry struct {
	val string
	exp time.Time
}

// cacheTTL keeps a resolved secret in-process just long enough that a burst of
// launches does not hit KMS once per credential, but short enough that a rotated
// key is picked up promptly.
const cacheTTL = 60 * time.Second

// SetVault installs the process vault and clears the resolve cache. Configure
// calls it from the environment; tests call it with a fake.
func SetVault(v Vault) {
	mu.Lock()
	vault = v
	mu.Unlock()
	cacheMu.Lock()
	cache = map[string]cacheEntry{}
	cacheMu.Unlock()
}

// Configured reports whether a vault is wired. When false, sealing is skipped
// and the plaintext column stays in force.
func Configured() bool {
	mu.RLock()
	defer mu.RUnlock()
	return vault != nil
}

// Configure wires the process vault from the environment when COMPUTE_KMS_NODES
// and COMPUTE_KMS_TOKEN are present. The client derives an org CEK client-side
// from COMPUTE_KMS_TOKEN (the service secret) and COMPUTE_KMS_ORG and unlocks once,
// so compute seals and unseals unattended. Absent config leaves the vault nil and
// returns nil — the plaintext path stays in force. A present-but-malformed
// config, or an unlock failure, returns an error so a half-provisioned
// deployment fails loudly rather than silently storing plaintext.
func Configure() error {
	nodes := splitCSV(os.Getenv("COMPUTE_KMS_NODES"))
	token := strings.TrimSpace(os.Getenv("COMPUTE_KMS_TOKEN"))
	if len(nodes) == 0 || token == "" {
		return nil
	}
	org := strings.TrimSpace(os.Getenv("COMPUTE_KMS_ORG"))
	if org == "" {
		org = "hanzo"
	}
	threshold := len(nodes)
	if t := strings.TrimSpace(os.Getenv("COMPUTE_KMS_THRESHOLD")); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 {
			return fmt.Errorf("kms: COMPUTE_KMS_THRESHOLD %q is not a positive integer", t)
		}
		threshold = n
	}
	c, err := sdk.NewClient(sdk.Config{Nodes: nodes, OrgSlug: org, Threshold: threshold})
	if err != nil {
		return fmt.Errorf("kms: client: %w", err)
	}
	if err := c.Unlock(token); err != nil {
		return fmt.Errorf("kms: unlock: %w", err)
	}
	SetVault(c)
	return nil
}

// Seal stores plaintext under key and returns the reference to persist in place
// of it, so the row holds a pointer and never the secret.
func Seal(key, plaintext string) (Ref, error) {
	mu.RLock()
	v := vault
	mu.RUnlock()
	if v == nil {
		return Ref{}, ErrNotConfigured
	}
	if err := v.Set(key, []byte(plaintext)); err != nil {
		return Ref{}, err
	}
	invalidate(key)
	return Ref{Path: key}, nil
}

// Open resolves a reference back to its plaintext, caching briefly so a burst of
// launches does not fetch the same key once per credential.
func Open(ref Ref) (string, error) {
	mu.RLock()
	v := vault
	mu.RUnlock()
	if v == nil {
		return "", ErrNotConfigured
	}
	if s, ok := getCache(ref.Path); ok {
		return s, nil
	}
	b, err := v.Get(ref.Path)
	if err != nil {
		return "", err
	}
	putCache(ref.Path, string(b))
	return string(b), nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getCache(key string) (string, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	e, ok := cache[key]
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	return e.val, true
}

func putCache(key, val string) {
	cacheMu.Lock()
	cache[key] = cacheEntry{val: val, exp: time.Now().Add(cacheTTL)}
	cacheMu.Unlock()
}

func invalidate(key string) {
	cacheMu.Lock()
	delete(cache, key)
	cacheMu.Unlock()
}
