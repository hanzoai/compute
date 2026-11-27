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

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/util"
)

// One machine's COMPUTE surface, reached over the Zero Trust fabric.
//
// A linked Mac publishes `hanzod.hanzo.zt → 127.0.0.1:3690` with
// `hanzo link host` (host mode). Host mode cannot intercept DNS, and neither
// can we: `.zt` is a private namespace. The caller of this op therefore never
// resolves a name — it dials the published service through the fabric with
// visor's IAM identity, and tunnels HTTP to hanzod's /v1/computer. Nothing is
// published on a public port and no privileged DNS change is involved.
//
// The op is a BYTE-FOR-BYTE tunnel. The upstream status, Content-Type and body
// are passed back verbatim: a screenshot is pixels, not a JSON field to be
// re-encoded. zip's typed seam marshals its Out as JSON, so the HTTP route
// installs a small response writer (routers/computer_wire.go) that replaces
// that body with the upstream bytes AFTER the typed handler has run. The typed
// In/Out still project into OpenAPI, MCP, the CLI and the call plane, where the
// body is carried as base64 instead of raw bytes.
//
// NO SENSITIVE LOGGING. This surface carries the arguments to a click or a
// keystroke, screenshots, and possibly a credential. Nothing here logs them,
// and object.NewRecord redacts the request body for this exact address — the
// audit recorder would otherwise persist every typed character in the clear.

const (
	computerApprovalHeader = "X-Hanzo-Approve"
	computerServicePrefix  = "hanzod."
	computerServiceSuffix  = ".zt"
	computerPath           = "/v1/computer"
	computerMaxBody        = 64 << 20 // a screen is large; an unbounded read is not
)

// ComputerIn is the wire input: the machine is addressed by (owner, name) in
// the URL, the approval evidence arrives BOTH as a header and in the body, and
// the rest of the body is forwarded untouched.
//
// owner is declared by the embedded caller (url:"owner") rather than a second
// field, so there is ONE reading of the address's tenant. The path is not
// trusted for identity: the handler checks it against the resolved principal
// before doing anything.
type ComputerIn struct {
	// Name is the machine's name within the caller's org, from the URL.
	Name string `json:"-" url:"name"`
	// Approval is the body half of the evidence Cloud gathered before an act op.
	Approval bool `json:"approval"`
	// ApprovalHeader is the header half of the same evidence. Kept as a string
	// so a forwarded value is not reinterpreted and then re-spelled.
	ApprovalHeader string `json:"-" header:"X-Hanzo-Approve"`
	// Raw is the exact request body, captured by UnmarshalJSON so forwarding
	// does not depend on knowing every field hanzod may add tomorrow.
	Raw json.RawMessage `json:"-"`
	caller
}

// UnmarshalJSON captures the body before decoding the one field we act on.
// Re-encoding would drop unknown fields and could change the approval field's
// spelling, which is precisely what must arrive intact at hanzod.
func (in *ComputerIn) UnmarshalJSON(b []byte) error {
	in.Raw = append(json.RawMessage(nil), b...)
	type plain ComputerIn
	return json.Unmarshal(b, (*plain)(in))
}

// ComputerOut is what the typed projection says the op answers. Over REST the
// response writer replaces the JSON with the upstream bytes; over MCP, the CLI
// and zip.Call — which have no raw response to write — this is the answer,
// with Body base64-encoded as JSON requires.
type ComputerOut struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        []byte `json:"body"`
}

// ComputerResponseKey and ComputerResponse are the per-request hand-off from a
// typed handler to the HTTP response writer. The writer stores the slot on the
// request's context.Context (see RawComputer), which zip wraps before handing
// the typed handler its ctx — so ctx.Value reaches the same slot.
type ComputerResponseKey struct{}

type ComputerResponse struct {
	Status      int
	ContentType string
	Body        []byte
	Raw         bool
}

// ComputerResponseOf reads the slot installed by the response writer, or nil
// when this op was reached over a transport that has no raw HTTP response.
func ComputerResponseOf(ctx context.Context) *ComputerResponse {
	r, _ := ctx.Value(ComputerResponseKey{}).(*ComputerResponse)
	return r
}

// Computer forwards one computer-use operation to the caller's machine over
// the fabric and answers with the upstream result unchanged.
//
// Example: {"approval":true,"action":"screenshot"}
// Response: {"status":200,"contentType":"application/json","body":"eyJvayI6dHJ1ZX0="}
func Computer(ctx context.Context, in *ComputerIn) (*ComputerOut, error) {
	user, org := principal(in.Authorization, in.Owner)
	org = strings.TrimSpace(org)
	if org == "" {
		return nil, zip.ErrForbidden("unauthorized: no org context")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("machine name is required")
	}
	// The URL names the tenant, but the credential decides it. A path that
	// names someone else's org is refused instead of quietly acting on the
	// caller's same-named machine — the two must agree before any work runs.
	if strings.TrimSpace(in.Owner) != org {
		return nil, zip.ErrForbidden("machine belongs to a different owner")
	}
	if err := authorize(user, util.GetIdFromOwnerAndName(org, name)); err != nil {
		return nil, err
	}

	machineId := util.GetIdFromOwnerAndName(org, name)
	machine, err := object.GetMachine(machineId)
	if err != nil {
		// Never echo the store error: it may name rows or credentials from
		// another tenant, and this handler's caller is only told the outcome.
		return nil, zip.ErrInternal("machine lookup failed")
	}
	if machine == nil {
		return nil, zip.ErrNotFound("machine not found")
	}

	dialer := service.CurrentZTDialer()
	if dialer == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"machine computer service unavailable: no Zero Trust identity")
	}
	serviceName := computerServicePrefix + org + computerServiceSuffix
	reply, err := tunnelComputer(ctx, dialer, serviceName, computerBody(in), in.ApprovalHeader)
	if err != nil {
		return nil, err
	}
	if slot := ComputerResponseOf(ctx); slot != nil {
		slot.Status, slot.ContentType, slot.Body, slot.Raw = reply.Status, reply.ContentType, reply.Body, true
	}
	return reply, nil
}

// computerBody is the body hanzod receives. The raw bytes win: they are the
// caller's exact op, approval field included. A typed in-process caller (which
// has no raw body) still gets a well-formed body with its approval spelled out.
func computerBody(in *ComputerIn) []byte {
	if len(in.Raw) != 0 {
		return []byte(in.Raw)
	}
	b, err := json.Marshal(struct {
		Approval bool `json:"approval"`
	}{Approval: in.Approval})
	if err != nil {
		return []byte(`{"approval":false}`)
	}
	return b
}

// tunnelComputer runs one HTTP request over a fabric connection. The transport
// is per request and closed on return — the SDK's zt.Context is what is cached
// (service.lazyZT), not an HTTP pool whose sockets would outlive the caller's
// authority. The approval header is forwarded verbatim; arguments and answers
// never appear in an error or a log line.
func tunnelComputer(ctx context.Context, dialer service.ZTDialer, serviceName string, body []byte, approval string) (*ComputerOut, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return dialer.Dial(serviceName)
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Minute}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+serviceName+computerPath, bytes.NewReader(body))
	if err != nil {
		return nil, zip.ErrInternal("machine computer request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	if approval != "" {
		req.Header.Set(computerApprovalHeader, approval)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"machine computer service unavailable: %s", serviceName)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, computerMaxBody))
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "machine computer service did not answer")
	}
	return &ComputerOut{
		Status:      res.StatusCode,
		ContentType: res.Header.Get("Content-Type"),
		Body:        raw,
	}, nil
}
