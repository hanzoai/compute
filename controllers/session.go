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
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

// A SESSION is one remote-access connection to a machine, and the row is the
// record of it: who connected, from where, when it started and ended, what was
// recorded. Six TYPED ops at one noun, with the method carrying the verb:
//
//	GET    /v1/sessions                             list one org's sessions
//	GET    /v1/sessions/:owner/:name                read one
//	PUT    /v1/sessions/:owner/:name                replace one
//	DELETE /v1/sessions/:owner/:name                remove one
//	PUT    /v1/sessions/:owner/:name/connection     the connection is up
//	DELETE /v1/sessions/:owner/:name/connection     force it down
//
// A session's identity is the pair (owner, name), so both are path segments —
// the same shape Hanzo IAM addresses its own owner/name entities with. They used
// to travel as one `?id=owner/name` query parameter on five verb addresses, so
// the thing being addressed was in the query and the operation was in the path,
// which is backwards.
//
// `connection` is a SUB-RESOURCE and not a status field, because the two DELETEs
// remove two different things: DELETE on the session removes the RECORD, while
// DELETE on its connection tears down the live guacamole tunnel
// (object.CloseSession reaches into GlobalSessionManager, writes a disconnect to
// the socket and to every observer) and LEAVES the record, closed. A
// `PUT .../status` door would also take `no_connect` and `connecting`, two of the
// four values of that column, and do nothing with either — a door whose
// behaviour depends on a body value is the verb in the path moved into JSON.

// Sessions is one org's sessions and how many there are in total — the count is
// the whole set matching the filter, not the page, so a caller paging through
// knows when to stop.
type Sessions struct {
	Sessions []*object.Session `json:"sessions"`
	Count    int64             `json:"count"`
}

// SessionQuery addresses the collection: whose sessions, filtered how, one page
// at a time. Every field rides the URL, so none of them is `json`-bound: a
// listing has no body.
type SessionQuery struct {
	// Owner is the org whose sessions to read. Absent means the caller's own —
	// unstated is not a licence to read every tenant.
	Owner string `json:"-" url:"owner"`
	// Status filters by connection state (connected, disconnected, connecting,
	// no_connect). Absent means every state.
	Status string `json:"-" url:"status"`
	// Page is 1-based. With no PageSize the whole set is returned.
	Page int `json:"-" url:"page"`
	// PageSize is how many rows one page holds. 0 means no pagination.
	PageSize int `json:"-" url:"pageSize"`
	// Field and Value are a substring filter on one whitelisted column.
	Field string `json:"-" url:"field"`
	Value string `json:"-" url:"value"`
	// SortField and SortOrder order the result; SortOrder is "ascend" or
	// "descend". An unknown column falls back to created_time.
	SortField string `json:"-" url:"sortField"`
	SortOrder string `json:"-" url:"sortOrder"`
	bearer
}

// SessionRef addresses ONE session by the pair that identifies it. Both halves
// come from the path, which is the authority: zip binds path over query over
// body, so a body cannot name a session other than the one the URL did.
type SessionRef struct {
	// Owner is the org the session belongs to.
	Owner string `json:"owner"`
	// Name is the session's own name within that org.
	Name string `json:"name"`
	bearer
}

// SessionEdit is the session as the caller wants it stored, at the address that
// says which one. It EMBEDS the model rather than restating its fields, so the
// wire and the row cannot drift; the path's owner/name overwrite the body's,
// which is what makes the URL the addressing authority here too.
type SessionEdit struct {
	object.Session
	bearer
}

// bearer is the identity an op declares when its target is stated by the
// ADDRESS: the forwarded IAM Bearer, and nothing else. It is `json:"-"` because
// it rides a header and must not be a field a caller can also put in a body.
type bearer struct {
	Authorization string `json:"-" header:"Authorization"`
}

// user resolves the signed-in principal from the declared Bearer, or nil for the
// service subject (Basic clientId/clientSecret), which ApiFilter has already
// authenticated as subOwner=="app" before any of these handlers run.
func (b bearer) user() *iamsdk.User { return object.GetBearerUser(b.Authorization) }

// admit is the tenant rule for a session: it belongs to exactly ONE org, the
// address says which, and this decides whether the caller may act on that org.
// It mirrors authz.IsAllowed's subject model, one layer in — which is where the
// check has to live now that the target is in the path rather than in `?id`, the
// only place the ABAC filter can read it from.
//
//   - the service subject (no Bearer; ApiFilter authenticated it as "app") is the
//     operator and reaches every org;
//   - a signed-in admin (the `built-in` org, or IsAdmin) reaches every org;
//   - a signed-in user reaches its own;
//   - forbidden, deleted, or anyone else reaches none.
func admit(u *iamsdk.User, owner string) error {
	if owner == "" {
		return zip.ErrBadRequest("session owner is required")
	}
	if u == nil {
		return nil
	}
	if u.IsForbidden || u.IsDeleted {
		return zip.ErrForbidden("Unauthorized operation")
	}
	if u.Owner == "built-in" || u.IsAdmin {
		return nil
	}
	if u.Owner == owner {
		return nil
	}
	return zip.ErrForbidden("Unauthorized operation: session belongs to a different owner")
}

// scope answers WHICH org a listing reads, and it never answers "all of them by
// accident": under the Base backend an empty owner resolves to the `_global`
// database, which holds no tenant's sessions, so an unresolved scope used to
// come back as an empty page rather than as an error. A stated org the caller
// may not read is refused rather than quietly replaced with its own.
func scope(u *iamsdk.User, stated string) (string, error) {
	stated = strings.TrimSpace(stated)
	if u == nil {
		if stated == "" {
			return "", zip.ErrBadRequest("owner is required")
		}
		return stated, nil
	}
	if u.IsForbidden || u.IsDeleted {
		return "", zip.ErrForbidden("Unauthorized operation")
	}
	if u.Owner == "built-in" || u.IsAdmin {
		if stated != "" {
			return stated, nil
		}
		return u.Owner, nil
	}
	if stated != "" && stated != u.Owner {
		return "", zip.ErrForbidden("Unauthorized operation: cannot read another org's sessions")
	}
	if u.Owner == "" {
		return "", zip.ErrForbidden("unauthorized: no org context")
	}
	return u.Owner, nil
}

// ListSessions returns one org's sessions, newest first, filtered and paged.
//
// One query serves both the paged and the whole-set read (offset and limit of -1
// drop the LIMIT clause), so the filters cannot apply to one and be silently
// ignored by the other — which is what the two branches this replaces did with
// status, field, value and the sort.
//
// Response: {"sessions": [{"owner": "acme", "name": "3f2b…", "status": "connected"}], "count": 1}
func ListSessions(_ context.Context, in *SessionQuery) (*Sessions, error) {
	owner, err := scope(in.user(), in.Owner)
	if err != nil {
		return nil, err
	}

	offset, limit := -1, -1
	if in.PageSize > 0 {
		page := in.Page
		if page < 1 {
			page = 1
		}
		offset, limit = (page-1)*in.PageSize, in.PageSize
	}

	sessions, err := object.GetPaginationSessions(owner, in.Status, offset, limit, in.Field, in.Value, in.SortField, in.SortOrder)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if sessions == nil {
		sessions = []*object.Session{}
	}

	// Unpaged, the page IS the set, so counting it again would be a second query
	// that can only agree with len().
	count := int64(len(sessions))
	if limit > 0 {
		if count, err = object.GetSessionCount(owner, in.Status, in.Field, in.Value); err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
		}
	}
	return &Sessions{Sessions: sessions, Count: count}, nil
}

// GetSession returns one session, or 404 when there is none at that address.
// Absent is a status and not a 200 carrying null: a caller that has to inspect
// the fields of a success to learn it got nothing is one that will forget to.
func GetSession(_ context.Context, in *SessionRef) (*object.Session, error) {
	if err := admit(in.user(), in.Owner); err != nil {
		return nil, err
	}
	session, err := object.GetConnSession(sessionId(in.Owner, in.Name))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if session == nil {
		return nil, zip.ErrNotFound("no such session")
	}
	return session, nil
}

// ReplaceSession stores the session the caller sent at the address that names
// it, and answers with the row as stored. Every column is written, so this is a
// replace and PUT is what it is called.
func ReplaceSession(_ context.Context, in *SessionEdit) (*object.Session, error) {
	if err := admit(in.user(), in.Owner); err != nil {
		return nil, err
	}
	id := sessionId(in.Owner, in.Name)
	session := in.Session
	found, err := object.UpdateSession(id, &session)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if !found {
		return nil, zip.ErrNotFound("no such session")
	}
	stored, err := object.GetConnSession(id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return stored, nil
}

// DeleteSession removes the record and answers 204. Idempotent: a session that
// is already gone is in the state the caller asked for.
func DeleteSession(_ context.Context, in *SessionRef) (*struct{}, error) {
	if err := admit(in.user(), in.Owner); err != nil {
		return nil, err
	}
	if _, err := object.DeleteSession(&object.Session{Owner: in.Owner, Name: in.Name}); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}

// ConnectSession records that the connection is up: the session moves to
// connected and its start time is stamped. The guacamole client reports this
// once its own handshake completes — the tunnel is what establishes the
// connection, this is what dates it.
//
// It is the one session address that answers without a credential, and
// deliberately: the tunnel routes the reporting client came through
// (add-asset-tunnel, get-asset-tunnel) are open in the policy, so that client
// holds nothing to authenticate with. It carries no reader's authority — it can
// only stamp a session that already exists — and it is the same admission the
// address it replaces had (authz's `p, *, *, POST, /v1/start-session, *, *`).
func ConnectSession(_ context.Context, in *SessionRef) (*object.Session, error) {
	id := sessionId(in.Owner, in.Name)
	found, err := object.UpdateSession(id, &object.Session{
		Status:    object.Connected,
		StartTime: util.GetCurrentTime(),
	}, "status", "start_time")
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if !found {
		return nil, zip.ErrNotFound("no such session")
	}
	return object.GetConnSession(id)
}

// DisconnectSession forces the connection down: the live guacamole tunnel and
// every observer of it are closed, and the record is left disconnected with the
// reason. The session RECORD stays — that is the difference between this and a
// DELETE of the session itself, and the reason the connection is addressable at
// all.
//
// Idempotent, so 204: a session with no live tunnel is already in the asked-for
// state.
func DisconnectSession(_ context.Context, in *SessionRef) (*struct{}, error) {
	if err := admit(in.user(), in.Owner); err != nil {
		return nil, err
	}
	if err := object.CloseSession(sessionId(in.Owner, in.Name), ForcedDisconnect, "The administrator forcibly closes the session"); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return nil, nil
}

// sessionId is the `owner/name` the store addresses a row by, built from the two
// path segments and nowhere else.
func sessionId(owner, name string) string {
	return util.GetIdFromOwnerAndName(strings.TrimSpace(owner), strings.TrimSpace(name))
}

// AddSession is deliberately NOT renamed onto POST /v1/sessions, and it is the
// one address of this family that keeps its verb.
//
// A session is the RECORD OF a connection, not a thing that is declared. The one
// door that mints one is the tunnel handshake (object.CreateSession, from
// AddAssetTunnel), which derives every field that matters — owner, name,
// protocol, asset, status — from the machine being reached and the clock. This
// takes all of them from the body instead, including Recording, Reviewed and
// CommandCount, which are the fields that make the row evidence. Nothing calls
// it: not the console, not hanzoai/cloud.
//
// Renaming it would publish a second way to create a session, as a typed op with
// a schema, an MCP tool and an SDK method — a nicer address for a door that
// should not be a second door. Whether it stays at all is a decision about who
// may write an audit row, which is not an addressing decision.
//
// @Title AddSession
// @Tag Session API
// @Description add session
// @Param   body    body   object.Session true "The session object"
// @Success 200 {object} Response
// @router /add-session [post]
func (c *ApiController) AddSession() {
	var session object.Session
	err := json.Unmarshal(c.Ctx.Body(), &session)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Data["json"] = wrapActionResponse(object.AddSession(&session))
	c.ServeJSON()
}
