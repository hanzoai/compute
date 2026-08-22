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

// A VOLUME: block storage the caller's org owns, provisioned through one of its
// registered providers. Seven TYPED ops — the entry in the registry that REST,
// OpenAPI, MCP, the CLI and the by-name call plane all project from.
//
// ONE noun, ONE address, and the METHOD carries the verb:
//
//	GET    /v1/volumes                   list the caller org's volumes
//	POST   /v1/volumes                   provision one
//	GET    /v1/volumes/:id               read one
//	DELETE /v1/volumes/:id               destroy one
//	PATCH  /v1/volumes/:id               change what is changeable — the size
//	PUT    /v1/volumes/:id/attachment    attach it to a machine (idempotent)
//	DELETE /v1/volumes/:id/attachment    detach it
//
// ATTACHMENT IS A SUB-RESOURCE, NOT A FIELD, and the deciding measurement is
// detach. As a field it is `machine`, so removing the relation has to be spelled
// as some sentinel value — an empty string in a partial update, which is
// indistinguishable from "I did not mention this field". As a sub-resource it
// EXISTS or it does not, PUT makes it and DELETE removes it, and nothing has to
// be encoded in a value. A machine's agent binding is the same shape at
// /v1/machines/:id/agent; this is that pattern, not a second one.
//
// SIZE IS A FIELD, so it is PATCH on the volume rather than a /size address.
// Minting a noun for a scalar would give the one changeable property its own
// resource and leave the next changeable property looking for a second one.
//
// :id IS THE VOLUME'S NAME within the org — the row's own key (the primary key
// is owner+name), which is what the list answers and what a caller can hold. The
// provider and the cloud's id are read from that row, so neither is a parameter
// any more. They used to be: every write took `?provider=`, and `?name=` meant
// the row's name on the read and the CLOUD's id on the writes, so one spelling
// addressed two different things depending on the verb.
//
// Every handler takes its tenant from ONE place: `principal`, the same rule the
// machine, node-pool and cluster surfaces run. It used to take it from two. The
// authorization filter derives the object's owner from `?id=` or the request
// BODY, while these handlers read `?owner=` — so a write naming the caller in
// the body and a victim in the query cleared authorization against the caller
// and then provisioned against the victim's cloud credentials, onto the victim's
// invoice.
package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/util"
)

// The two states a recorded volume is in. Written once here because the row and
// the provider adapters both use these exact strings.
const (
	volumeAvailable = "Available"
	volumeAttached  = "Attached"
)

// VolumeRef addresses one of the caller org's volumes: the whole input of a
// read, a destroy and a detach, because a volume's name and the caller are
// everything there is to say.
type VolumeRef struct {
	// Id is the volume's name within the caller's org, from the URL path. The
	// OWNER half is never taken from the caller — see volume.
	Id string `json:"id"`
	caller
}

// VolumeNew provisions a volume on one of the org's registered providers.
//
// It EMBEDS the provider's own spec rather than restating its fields: one shape
// published under two names is two schemas a generated client has to learn, and
// the second one drifts.
type VolumeNew struct {
	// Provider is the org's registered provider to provision on. It rides the
	// URL and is `json:"-"`, so it is never a field a body can also carry — the
	// same rule the caller's own two inputs obey.
	Provider string `json:"-" url:"provider" validate:"required"`
	service.CreateVolumeSpec
	caller
}

// VolumeAttach attaches a volume to a machine.
type VolumeAttach struct {
	// Id is the volume to attach, from the URL path.
	Id string `json:"id"`
	// Machine is the cloud's id for the machine to attach it to.
	Machine string `json:"machine" validate:"required"`
	caller
}

// VolumeResize is the partial update a volume accepts: its size, in GB. Growth
// only — no cloud this service reaches shrinks a volume.
type VolumeResize struct {
	// Id is the volume to resize, from the URL path.
	Id string `json:"id"`
	// Size is the new size in GB. min=1 rather than required, because it is one
	// rule that covers both a missing field and a nonsense one — an absent size
	// binds to 0 and fails the same bound a negative does.
	Size int `json:"size" validate:"min=1"`
	caller
}

// Volumes is every volume the caller org owns.
type Volumes struct {
	// Volumes is one row per volume, newest first.
	Volumes []*object.Volume `json:"volumes"`
}

// principalOrg is `principal` plus the refusal, in one place, so no handler
// carries its own copy of what an empty org means. An authenticated principal's
// org is authoritative and NOT overridable by a client-supplied ?owner; only an
// unauthenticated service subject, which ApiFilter has already admitted as
// subOwner=="app", may name one.
func principalOrg(c caller) (string, error) {
	_, org := principal(c.Authorization, c.Owner)
	if org == "" {
		return "", zip.ErrForbidden("unauthorized: no org context")
	}
	return org, nil
}

// volume resolves a request into the caller org's volume ROW, which is where the
// provider and the cloud's id live. A user therefore cannot address another
// org's volume by crafting the path: the owner half of the key is always the
// caller's own resolved org.
//
// The row is also the tenancy control the cloud calls rest on. Every one of them
// is made with credentials looked up in THIS org, against an id read from a row
// THIS org owns — so an id belonging to someone else resolves to nothing here
// and the call is never made.
func volume(c caller, id string) (string, *object.Volume, error) {
	org, err := principalOrg(c)
	if err != nil {
		return "", nil, err
	}
	name := strings.TrimSpace(id)
	if name == "" {
		return "", nil, zip.ErrBadRequest("volume id is required")
	}
	v, err := object.GetVolume(org, name)
	if err != nil {
		return "", nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if v == nil {
		return "", nil, zip.ErrNotFound("volume not found")
	}
	return org, v, nil
}

// saveVolume writes back a row the cloud has just changed and answers with it.
//
// The cloud calls report only success, so without this the stored row keeps the
// size and the machine it had BEFORE the call, and every later read serves that
// stale answer. A PUT that creates an attachment and then hands back a volume
// attached to nothing is not a resource, it is a rumour.
func saveVolume(v *object.Volume) (*object.Volume, error) {
	v.UpdatedTime = util.GetCurrentTime()
	if _, err := object.UpdateVolume(v.Owner, v.Name, v); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return v, nil
}

// ListVolumes returns every volume in the caller's org, newest first.
//
// Response: {"volumes": [{"owner": "acme", "name": "data-a", "size": 100, "state": "Available"}]}
func ListVolumes(_ context.Context, in *Scope) (*Volumes, error) {
	org, err := principalOrg(in.caller)
	if err != nil {
		return nil, err
	}
	volumes, err := object.GetVolumes(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if volumes == nil {
		volumes = []*object.Volume{}
	}
	return &Volumes{Volumes: volumes}, nil
}

// CreateVolume provisions a block volume on one of the caller org's registered
// providers and records it. The provider is resolved in the CALLER's org, never
// in one a request named.
//
// Example: POST /v1/volumes?provider=do {"name": "data-a", "size": 100, "region": "nyc3", "format": "ext4"}
func CreateVolume(_ context.Context, in *VolumeNew) (*object.Volume, error) {
	org, err := principalOrg(in.caller)
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		return nil, zip.ErrBadRequest("provider is required")
	}
	spec := in.CreateVolumeSpec
	v, err := object.CreateVolumeCloud(org, provider, &spec)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return v, nil
}

// GetVolume returns one of the caller org's volumes, or 404 when the org owns no
// volume by that name.
//
// Absent is 404 and not a 200 carrying null: a caller that has to inspect the
// fields of a success to discover a miss is one that will forget to.
func GetVolume(_ context.Context, in *VolumeRef) (*object.Volume, error) {
	_, v, err := volume(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// DeleteVolume destroys one of the caller org's volumes at its provider and
// drops the row. 204 — a destroy has nothing to say.
//
// It reaches only volumes this service RECORDED. The old address took a raw
// cloud id and a provider name, so it could destroy anything in the org's cloud
// account, including storage visor never provisioned and cannot see.
func DeleteVolume(_ context.Context, in *VolumeRef) (*struct{}, error) {
	org, v, err := volume(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	if err := object.DeleteVolumeCloud(org, v.Provider, v.Id); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}

// AttachVolume attaches one of the caller org's volumes to a machine and answers
// with the volume in its new state.
//
// Idempotent — PUT, because re-attaching to the same machine is the state the
// caller asked for, not a second attachment; naming a different machine replaces
// the relation, since a volume is attached to at most one.
func AttachVolume(_ context.Context, in *VolumeAttach) (*object.Volume, error) {
	org, v, err := volume(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	machine := strings.TrimSpace(in.Machine)
	if machine == "" {
		return nil, zip.ErrBadRequest("machine is required")
	}
	if err := object.AttachVolumeCloud(org, v.Provider, v.Id, machine); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	v.Machine = machine
	v.State = volumeAttached
	return saveVolume(v)
}

// DetachVolume removes a volume's attachment and answers 204. The volume stays —
// detaching releases it from the machine, it does not destroy the storage, so
// the two lifecycles stay orthogonal.
//
// Idempotent: a volume that was attached to nothing is already in the asked-for
// state.
func DetachVolume(_ context.Context, in *VolumeRef) (*struct{}, error) {
	org, v, err := volume(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	if err := object.DetachVolumeCloud(org, v.Provider, v.Id); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	v.Machine = ""
	v.State = volumeAvailable
	if _, err := saveVolume(v); err != nil {
		return nil, err
	}
	return nil, nil
}

// ResizeVolume grows one of the caller org's volumes and answers with it.
//
// PATCH on the volume rather than PUT: size is the one property a provisioned
// volume will accept a change to, and a PUT would claim that region, format and
// the rest can be replaced too.
func ResizeVolume(_ context.Context, in *VolumeResize) (*object.Volume, error) {
	org, v, err := volume(in.caller, in.Id)
	if err != nil {
		return nil, err
	}
	if err := object.ResizeVolumeCloud(org, v.Provider, v.Id, in.Size); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	v.Size = in.Size
	return saveVolume(v)
}
