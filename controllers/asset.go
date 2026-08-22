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

package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// An ASSET is a reachable machine visor can open a remote session on. Five
// TYPED ops at two addresses, and the METHOD carries the verb:
//
//	GET    /v1/assets                one page of the org's assets
//	POST   /v1/assets                create
//	GET    /v1/assets/:owner/:name   read one
//	PUT    /v1/assets/:owner/:name   replace one
//	DELETE /v1/assets/:owner/:name   remove one
//
// They were five addresses — get-assets, get-asset, add-asset, update-asset,
// delete-asset — so a client held one URL per verb and three of them took the
// same asset three different ways: ?id=owner/name on the read and the update, a
// whole asset in the body on the delete. An asset's identity is the (owner,
// name) pair, so the pair is the address, exactly as hanzoai/iam addresses a
// user.
//
// They are TYPED, so this noun is in the registry every projection reads —
// OpenAPI, MCP, CLI, the generated SDKs, the by-name call plane — rather than
// only on the wire. That means they DROP the {status,msg,data} envelope the
// rest of this package still answers: the answer is the value and the status is
// the outcome. Safe to break here because the callers are measured and local —
// hanzoai/cloud reaches no asset route, and visor's own web build is updated in
// the same change (web/src/backend/AssetBackend.js).
//
// The TENANT boundary is unchanged and still ApiFilter's: it resolves the
// object's owner from the request (routers/authz_filter.go, getObject) and
// admits a subject acting within its own org. The pair now arrives in the path
// rather than ?owner= / ?id=, which is the one thing that had to move with it.

// AssetRef addresses ONE asset. Both halves ride the URL path, which is the
// whole point of the address: a read or a delete names its target and carries
// no body at all.
type AssetRef struct {
	Owner string `json:"owner" validate:"required"`
	Name  string `json:"name" validate:"required"`
}

// AssetQuery reads the collection: whose assets, and how much of it.
//
// Page and PageSize are optional together — a request that names neither gets
// the whole org, which is what the asset tree asks for. Field and Value are the
// column filter the list page searches on; SortField and SortOrder order the
// result.
type AssetQuery struct {
	Owner     string `json:"owner"`
	Page      int    `json:"p"`
	PageSize  int    `json:"pageSize"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
}

// Assets is one page of the collection, and the size of the whole of it.
//
// Total is the count BEFORE paging, so a client can size a pager without asking
// twice. It used to ride a second field of the envelope (data2), which no
// schema described and no generated client could find.
type Assets struct {
	Assets []*object.Asset `json:"assets"`
	Total  int64           `json:"total"`
}

// AssetBody is a create: the asset states its own owner and name, because a
// create is the one write that has no address yet.
type AssetBody struct {
	Asset object.Asset `json:"asset"`
}

// AssetPut is a replace. The URL says WHICH asset and only the URL says it;
// `json:"-"` keeps the body from naming a second target, so the row authorized
// is the row written.
//
// Asset is what that row BECOMES, its own owner and name included — so renaming
// an asset stays a plain replace rather than a second verb. zip binds a URL
// segment onto a TOP-LEVEL field, reaching through embedding but not through a
// named struct field, which is why the address is stated here and the value is
// nested.
type AssetPut struct {
	Owner string `json:"-" url:"owner"`
	Name  string `json:"-" url:"name"`

	Asset object.Asset `json:"asset"`
}

// ListAssets returns one page of an org's assets, newest first, each with its
// remote password masked.
//
// Response: {"assets": [{"owner": "acme", "name": "db-1", "publicIp": "…"}], "total": 1}
func ListAssets(_ context.Context, in *AssetQuery) (*Assets, error) {
	owner := strings.TrimSpace(in.Owner)

	// No page asked for is the whole org: the asset tree wants every row and has
	// no pager to fill.
	if in.Page < 1 || in.PageSize < 1 {
		assets, err := object.GetMaskedAssets(object.GetAssets(owner))
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
		}
		return &Assets{Assets: nonNil(assets), Total: int64(len(assets))}, nil
	}

	total, err := object.GetAssetCount(owner, in.Field, in.Value)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	offset := (in.Page - 1) * in.PageSize
	assets, err := object.GetMaskedAssets(object.GetPaginationAssets(
		owner, offset, in.PageSize, in.Field, in.Value, in.SortField, in.SortOrder))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return &Assets{Assets: nonNil(assets), Total: total}, nil
}

// CreateAsset stores a new asset and answers 201 with the stored row.
func CreateAsset(_ context.Context, in *AssetBody) (*object.Asset, error) {
	asset := in.Asset
	if strings.TrimSpace(asset.Owner) == "" || strings.TrimSpace(asset.Name) == "" {
		return nil, zip.ErrBadRequest("an asset is named by an owner and a name")
	}
	added, err := object.AddAsset(&asset)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if !added {
		return nil, zip.ErrBadRequest("asset was not stored")
	}
	return object.GetMaskedAsset(&asset)
}

// GetAsset returns one asset with its remote password masked, or 404.
func GetAsset(_ context.Context, in *AssetRef) (*object.Asset, error) {
	asset, err := object.GetMaskedAsset(object.GetAsset(util.GetIdFromOwnerAndName(in.Owner, in.Name)))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// Absent is 404 and not a 200 carrying null: "there is no such asset" is a
	// fact about the address, and a caller that has to inspect the fields of a
	// success to discover a miss is one that will forget to.
	if asset == nil {
		return nil, zip.ErrNotFound("no such asset")
	}
	return asset, nil
}

// UpdateAsset replaces the addressed asset and answers the stored row, or 404
// when there is nothing at that address.
//
// A remote password of "***" is the masked value a read handed out, and means
// "keep the stored one" — so a client can round-trip an asset it read without
// ever holding the secret.
func UpdateAsset(_ context.Context, in *AssetPut) (*object.Asset, error) {
	id := util.GetIdFromOwnerAndName(in.Owner, in.Name)
	asset := in.Asset
	updated, err := object.UpdateAsset(id, &asset)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if !updated {
		return nil, zip.ErrNotFound("no such asset")
	}
	return object.GetMaskedAsset(&asset)
}

// DeleteAsset removes the addressed asset and answers 204.
//
// Idempotent: an address with nothing at it is already in the asked-for state,
// so it answers 204 too. The old wire said "Affected"/"Unaffected" in a 200
// body, which made every caller parse prose to learn a distinction none of them
// acted on.
func DeleteAsset(_ context.Context, in *AssetRef) (*struct{}, error) {
	if _, err := object.DeleteAsset(&object.Asset{Owner: in.Owner, Name: in.Name}); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}

// nonNil renders an empty result as [] rather than null, so a client never has
// to tell the two apart.
func nonNil(assets []*object.Asset) []*object.Asset {
	if assets == nil {
		return []*object.Asset{}
	}
	return assets
}
