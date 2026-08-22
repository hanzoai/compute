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

package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// A cloud PROVIDER: the credential and configuration one org holds for one
// cloud. Five TYPED ops, so this noun is in the registry every projection reads
// — OpenAPI, MCP, the CLI, the by-name call plane — rather than only on the
// wire.
//
// ONE noun, ONE address, and the METHOD carries the verb:
//
//	GET    /v1/providers               the org's providers, masked
//	POST   /v1/providers               register one
//	GET    /v1/providers/:owner/:name  read one
//	PUT    /v1/providers/:owner/:name  replace one
//	DELETE /v1/providers/:owner/:name  remove one
//
// A provider's identity is its PRIMARY KEY, the pair (Owner, Name), so that pair
// IS the address. `built-in/do` is the same string the retired ?id carried — the
// separator did not change, only which part of the URL holds it.
//
// The owner stays in the address rather than being taken from the principal,
// because it is what the authorization seam compares the subject against
// (routers.getObject → authz.IsAllowed's subOwner == objOwner). Moving it into
// the session would move the decision too.
//
// A secret is masked on the way out (object.GetMaskedProvider) and a masked
// secret sent back on a replace restores the stored one (object.UpdateProvider),
// so reading a provider and saving it again does not overwrite the credential
// with three asterisks.

// ProviderFilter is what a read of the collection carries: whose providers, and
// which page of them. An absent page or size answers the whole collection, which
// is what a caller that just wants the org's providers sends.
type ProviderFilter struct {
	// Owner is the org whose providers to answer with.
	Owner string `json:"owner"`
	// Page is the 1-based page number; PageSize is how many rows it holds.
	Page     int `json:"p"`
	PageSize int `json:"pageSize"`
	// Field and Value narrow the collection to rows whose Field equals Value.
	Field string `json:"field"`
	Value string `json:"value"`
	// SortField and SortOrder order it; empty is newest first.
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
}

// Providers is one page of the collection, and how many rows the whole
// collection holds under the same filter.
type Providers struct {
	Providers []*object.Provider `json:"providers"`
	Total     int64              `json:"total"`
}

// ProviderRef addresses ONE provider by the pair that is its primary key, both
// halves read from the URL path.
type ProviderRef struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// ProviderEdit is a replacement: WHICH provider, from the URL, and what it
// becomes, from the body.
//
// The record is a NESTED field rather than a flattened one, and that is the
// whole design. zip binds the URL over the TOP LEVEL only, so the two names stay
// two values: the one in the path is the provider being written, the one in the
// record is what it is called afterwards. Flattened, the path would overwrite the
// record and a provider could never be renamed — and renaming is how a provider
// gets its name at all, since one is created under a generated one.
//
// The OWNER is not like that. The handler writes the record into the org the
// ADDRESS names, whatever the body says, so a body cannot move a credential into
// another tenant.
type ProviderEdit struct {
	// Owner and Name address the provider being replaced, from the URL path.
	Owner string `json:"owner"`
	Name  string `json:"name"`
	// Provider is the state it takes on.
	Provider object.Provider `json:"provider"`
}

// providerId renders the (owner, name) pair as the id the store is keyed by, and
// refuses a half-formed one. object.GetProvider panics on an id that is not two
// tokens, so this is the guard that keeps a malformed address a 400.
func providerId(owner, name string) (string, error) {
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
	if owner == "" || name == "" {
		return "", zip.ErrBadRequest("a provider is addressed by owner and name")
	}
	return util.GetIdFromOwnerAndName(owner, name), nil
}

// ListProviders answers an org's providers with every secret masked.
//
// Response: {"providers": [{"owner": "acme", "name": "do", "type": "DigitalOcean", "clientSecret": "***"}], "total": 1}
func ListProviders(_ context.Context, in *ProviderFilter) (*Providers, error) {
	if in.Page <= 0 || in.PageSize <= 0 {
		providers, err := object.GetMaskedProviders(object.GetProviders(in.Owner))
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
		}
		return &Providers{Providers: providers, Total: int64(len(providers))}, nil
	}

	total, err := object.GetProviderCount(in.Owner, in.Field, in.Value)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// The offset is the definition of a page, and the input already carries the
	// two numbers as numbers — util.Paginate exists to parse a page out of a query
	// STRING and clamp it, which the typed input and the guard above have done.
	offset := (in.Page - 1) * in.PageSize
	providers, err := object.GetMaskedProviders(object.GetPaginationProviders(
		in.Owner, offset, in.PageSize, in.Field, in.Value, in.SortField, in.SortOrder))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return &Providers{Providers: providers, Total: total}, nil
}

// GetProvider answers ONE provider with its secret masked, or 404 when the org
// holds no provider by that name.
//
// Response: {"owner": "acme", "name": "do", "type": "DigitalOcean", "clientSecret": "***"}
func GetProvider(_ context.Context, in *ProviderRef) (*object.Provider, error) {
	id, err := providerId(in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	provider, err := object.GetMaskedProvider(object.GetProvider(id))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// Absent is 404, not a 200 carrying null: "this org holds no such provider" is
	// a fact about the address, and a caller that has to inspect a success to
	// learn it missed is one that will forget to.
	if provider == nil {
		return nil, zip.ErrNotFound("no such provider")
	}
	return provider, nil
}

// AddProvider registers a provider for an org and answers with it, masked.
//
// Example: {"owner": "acme", "name": "do", "type": "DigitalOcean", "clientSecret": "dop_v1_…", "region": "sfo3"}
func AddProvider(_ context.Context, in *object.Provider) (*object.Provider, error) {
	if _, err := providerId(in.Owner, in.Name); err != nil {
		return nil, err
	}
	if _, err := object.AddProvider(in); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return object.GetMaskedProvider(in)
}

// ReplaceProvider writes a provider's new state and answers with what is now
// stored, masked; 404 when the address holds nothing to replace.
//
// A masked secret — the "***" a read hands back — leaves the stored credential
// alone, so an edit that touched only the region does not blank the key.
//
// Example: PUT /v1/providers/acme/provider_kx3 {"provider": {"name": "do", "type": "DigitalOcean", "region": "sfo3"}}
func ReplaceProvider(_ context.Context, in *ProviderEdit) (*object.Provider, error) {
	id, err := providerId(in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	// The address is the authority on the tenant, and on the name when the body
	// supplies none: the record is written with all columns, so an absent name
	// would otherwise blank the primary key of the row it was meant to keep.
	in.Provider.Owner = in.Owner
	if strings.TrimSpace(in.Provider.Name) == "" {
		in.Provider.Name = in.Name
	}
	if _, err := object.UpdateProvider(id, &in.Provider); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// Read back from where it now lives, which a rename has moved.
	now, err := providerId(in.Provider.Owner, in.Provider.Name)
	if err != nil {
		return nil, err
	}
	updated, err := object.GetMaskedProvider(object.GetProvider(now))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if updated == nil {
		return nil, zip.ErrNotFound("no such provider")
	}
	return updated, nil
}

// RemoveProvider deletes a provider and answers 204.
//
// It is addressed by the URL alone: the old wire took the whole record in a body
// and read two fields out of it, which let a caller send a provider that differs
// from the one it deletes.
func RemoveProvider(_ context.Context, in *ProviderRef) (*struct{}, error) {
	if _, err := providerId(in.Owner, in.Name); err != nil {
		return nil, err
	}
	if _, err := object.DeleteProvider(&object.Provider{Owner: in.Owner, Name: in.Name}); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}
