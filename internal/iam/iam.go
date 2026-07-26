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

// Package iam is visor's INLINE identity client for the clean Hanzo IAM
// (hanzo.id). It replaces the retired Casdoor SDK (github.com/hanzoai/iam-v1):
// there is no vendored 218-field User and no SDK module — just standard OIDC.
//
//   - Token validation is JWKS verification against the issuer's published keys
//     at {ISSUER}/v1/iam/.well-known/jwks (RS256/ES256), with a fallback to the
//     KMS-synced certificate PEM when JWKS is unreachable. Identity is read
//     straight off the verified CLAIMS (owner/name/email/sub) — no user lookup
//     for the self case.
//   - GetOAuthToken is a standard OAuth2 authorization-code exchange against
//     {ISSUER}/v1/iam/oauth/access_token (the /v1/signin browser flow).
//   - AddUser is a single HTTP POST to {ISSUER}/v1/iam/add-user (bot service
//     accounts). It is the one identity WRITE visor makes; everything else is
//     read-from-claims.
//
// User carries ONLY the fields visor actually reads — not the full IAM user.
package iam

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// DefaultIssuer is the clean IAM endpoint used when none is configured.
const DefaultIssuer = "https://hanzo.id"

// User is the minimal identity visor reads off a verified token or writes when
// registering a bot service account. JSON tags match the IAM wire shape so the
// same struct decodes a JWT payload's embedded user claims and marshals an
// add-user body.
type User struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Tag         string `json:"tag,omitempty"`
	IsAdmin     bool   `json:"isAdmin,omitempty"`
	IsForbidden bool   `json:"isForbidden,omitempty"`
	IsDeleted   bool   `json:"isDeleted,omitempty"`
}

// GetId returns the IAM composite id "<owner>/<name>".
func (u User) GetId() string { return u.Owner + "/" + u.Name }

// Claims is the verified token payload: the embedded User identity claims, the
// standard registered claims (iss/sub/exp/…), and the raw access token the
// signin flow stashes into the session. The clean IAM mints these fields flat
// at the JWT top level, so both embedded structs decode from one payload.
type Claims struct {
	User
	AccessToken string `json:"accessToken,omitempty"`
	jwt.RegisteredClaims
}

// authConfig is the process-wide IAM configuration set once at startup by
// InitConfig (from app.conf / KMS). Endpoint is the brand serverUrl; Certificate
// is the KMS-synced signing cert PEM used only as a JWKS fallback.
type authConfig struct {
	Endpoint     string
	ClientId     string
	ClientSecret string
	Certificate  string
	Organization string
	Application  string
}

var (
	cfgMu     sync.RWMutex
	globalCfg authConfig
)

// InitConfig installs the process-wide IAM configuration. Signature matches the
// retired SDK's so call sites change only their import, not their arguments.
func InitConfig(endpoint, clientId, clientSecret, certificate, organizationName, applicationName string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	globalCfg = authConfig{
		Endpoint:     endpoint,
		ClientId:     clientId,
		ClientSecret: clientSecret,
		Certificate:  certificate,
		Organization: organizationName,
		Application:  applicationName,
	}
}

func config() authConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return globalCfg
}

// issuerEndpoint resolves the IAM base URL: the configured endpoint, else the
// IAM_ENDPOINT/IAM_ISSUER env, else DefaultIssuer. Never empty.
func issuerEndpoint() string {
	if e := strings.TrimSpace(config().Endpoint); e != "" {
		return e
	}
	if e := strings.TrimSpace(os.Getenv("IAM_ENDPOINT")); e != "" {
		return e
	}
	if e := strings.TrimSpace(os.Getenv("IAM_ISSUER")); e != "" {
		return e
	}
	return DefaultIssuer
}

// ParseJwtToken verifies a token's SIGNATURE against the IAM issuer's JWKS (the
// canonical public keys at {ISSUER}/v1/iam/.well-known/jwks), falling back to
// the configured certificate PEM when JWKS is unreachable, and returns the
// verified claims. A forged, tampered, or expired token is rejected.
func ParseJwtToken(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, keyFunc)
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("visor/iam: token is not valid")
	}
	return claims, nil
}

// keyFunc supplies the verification key for a token, keyed by its `kid`. It
// admits only the asymmetric algorithms the clean IAM mints (RS256/RS512 and
// ES256/ES512) — never a symmetric/`none` alg — then resolves the public key
// from JWKS first and the configured cert PEM second.
func keyFunc(token *jwt.Token) (interface{}, error) {
	switch token.Method.Alg() {
	case jwt.SigningMethodES256.Alg(), jwt.SigningMethodES512.Alg(),
		jwt.SigningMethodRS256.Alg(), jwt.SigningMethodRS512.Alg():
		kid, _ := token.Header["kid"].(string)
		if pk, jwksErr := jwksPublicKey(issuerEndpoint(), kid); jwksErr == nil {
			return pk, nil
		}
		if cert := config().Certificate; cert != "" {
			return publicKeyFromPEM([]byte(cert))
		}
		return nil, errors.New("visor/iam: no JWKS key and no certificate configured")
	default:
		return nil, fmt.Errorf("visor/iam: unsupported signing method: %v", token.Header["alg"])
	}
}

// publicKeyFromPEM parses either an X.509 CERTIFICATE PEM (the form IAM stores)
// or a raw PUBLIC KEY PEM, returning the contained RSA or ECDSA public key.
func publicKeyFromPEM(pemBytes []byte) (interface{}, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("visor/iam: not valid PEM")
	}
	if block.Type == "CERTIFICATE" {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("visor/iam: parse certificate: %w", err)
		}
		return cert.PublicKey, nil
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

// GetOAuthToken performs the OAuth2 authorization-code exchange for the /v1/signin
// browser flow, against the HIP-0111 canonical {ISSUER}/v1/iam/oauth/token (the
// path advertised by the issuer's OIDC discovery document; the Casdoor-era
// /v1/iam/oauth/access_token alias is legacy and deliberately not used).
func GetOAuthToken(code, state string) (*oauth2.Token, error) {
	cfg := config()
	endpoint := issuerEndpoint()
	oauthCfg := oauth2.Config{
		ClientID:     cfg.ClientId,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   fmt.Sprintf("%s/v1/iam/oauth/authorize", endpoint),
			TokenURL:  fmt.Sprintf("%s/v1/iam/oauth/token", endpoint),
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	token, err := oauthCfg.Exchange(context.Background(), code)
	if err != nil {
		return token, err
	}
	if strings.HasPrefix(token.AccessToken, "error:") {
		return nil, errors.New(strings.TrimPrefix(token.AccessToken, "error: "))
	}
	return token, nil
}

// AddUser creates (or upserts) an IAM user via a single POST to
// {ISSUER}/v1/iam/add-user, authenticated with the app's client credentials.
// It is used only for best-effort bot service-account registration; the caller
// swallows the error, so a missing config or unreachable IAM never fails a launch.
func AddUser(user *User) (bool, error) {
	if user == nil {
		return false, errors.New("visor/iam: nil user")
	}
	cfg := config()
	if user.Owner == "" {
		user.Owner = cfg.Organization
	}
	body, err := json.Marshal(user)
	if err != nil {
		return false, err
	}
	reqURL := fmt.Sprintf("%s/v1/iam/add-user?id=%s", issuerEndpoint(), url.QueryEscape(user.GetId()))
	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.SetBasicAuth(cfg.ClientId, cfg.ClientSecret)
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
		return false, fmt.Errorf("visor/iam: add-user status %d: %s", resp.StatusCode, string(respBytes))
	}
	var env struct {
		Status string      `json:"status"`
		Msg    string      `json:"msg"`
		Data   interface{} `json:"data"`
	}
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return false, err
	}
	if env.Status != "ok" {
		return false, errors.New(env.Msg)
	}
	return env.Data == "Affected", nil
}

// --- JWKS fetch + cache (OIDC public signing keys) ---

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

type jwksCacheEntry struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

var (
	jwksCache   = map[string]jwksCacheEntry{}
	jwksCacheMu sync.RWMutex
)

const jwksTTL = 10 * time.Minute

func jwksURL(endpoint string) string {
	return strings.TrimRight(endpoint, "/") + "/v1/iam/.well-known/jwks"
}

func jwkToRSAPublicKey(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("visor/iam: jwk n decode: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("visor/iam: jwk e decode: %w", err)
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		e = 65537
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func fetchJWKS(endpoint string) (map[string]*rsa.PublicKey, error) {
	u := jwksURL(endpoint)

	jwksCacheMu.RLock()
	if ce, ok := jwksCache[u]; ok && time.Since(ce.fetchedAt) < jwksTTL {
		jwksCacheMu.RUnlock()
		return ce.keys, nil
	}
	jwksCacheMu.RUnlock()

	resp, err := http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("visor/iam: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("visor/iam: read jwks: %w", err)
	}
	var doc jwksDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("visor/iam: parse jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if !strings.EqualFold(k.Kty, "RSA") {
			continue
		}
		pk, err := jwkToRSAPublicKey(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pk
	}
	if len(keys) == 0 {
		return nil, errors.New("visor/iam: jwks had no usable RSA keys")
	}

	jwksCacheMu.Lock()
	jwksCache[u] = jwksCacheEntry{keys: keys, fetchedAt: time.Now()}
	jwksCacheMu.Unlock()
	return keys, nil
}

func jwksPublicKey(endpoint, kid string) (*rsa.PublicKey, error) {
	keys, err := fetchJWKS(endpoint)
	if err != nil {
		return nil, err
	}
	if kid != "" {
		if pk, ok := keys[kid]; ok {
			return pk, nil
		}
		return nil, fmt.Errorf("visor/iam: no jwks key for kid %q", kid)
	}
	if len(keys) == 1 {
		for _, pk := range keys {
			return pk, nil
		}
	}
	return nil, fmt.Errorf("visor/iam: kid required (jwks has %d keys)", len(keys))
}
