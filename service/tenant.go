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

// tenant.go is the ONE home for the org+project tenancy attribution shared by the
// resell compute surface (compute.go), the metering path (metering.go), and fleet
// billing (fleet_billing.go): how an org and a project are recovered from a
// machine's tags, encoded into a metering line, and validated so neither can ever
// corrupt the comma/colon-joined tag read-back or the commerce billing key.
//
// The org is the tenant boundary and the debit destination; the project is a
// second attribution dimension WITHIN the org. The gateway mints X-Project-Id
// (like X-Org-Id); the empty project is the org's DEFAULT project and preserves
// today's behavior exactly — no project tag is written and the metering actor
// stays the bare org, so every keyed surface is backward-compatible.
package service

import "strings"

// orgFromTag recovers the owning org from a machine's comma-joined tag string (as
// getMachineFromDroplet builds it: "k1:v1,k2:v2,"): the value after "hanzo-org:".
// Empty when the machine carries no org tag — such a machine is unattributable and
// is skipped rather than billed to a wrong tenant. This is the authoritative org
// read-back; projectFromTag mirrors it for the project dimension.
func orgFromTag(tags string) string { return tagValue(tags, orgTagKey) }

// projectFromTag recovers the owning project from a machine's tags: the value
// after "hanzo-project:". Empty == the org's DEFAULT project (a machine launched
// without a project, or a legacy machine that predates the project dimension), so
// an empty result is the backward-compatible default, never an error.
func projectFromTag(tags string) string { return tagValue(tags, projectTagKey) }

// MeterActor encodes org+project into the commerce metering Actor — the
// audit-trail identity recorded on every usage transaction. It NEVER changes which
// balance is gated or debited: that is ALWAYS the org (the Usage.User billing
// key), so one org credit covers all its projects. Actor only ATTRIBUTES the line,
// which is what makes spend reportable per project. It is the ONE place org+project
// is folded into a metering line, shared by the launch debit, the recurring sweep,
// and every fleet-billing tier.
//
// Empty project == the org's default project == today's behavior (Actor == org),
// so threading project through the existing launch and sweep debits is a no-op for
// any caller that does not set X-Project-Id. A named project yields "org/project"
// (the same org/sub shape commerce already documents for Actor).
func MeterActor(org, project string) string {
	if project == "" {
		return org
	}
	return org + "/" + project
}

// validOrgSlug bounds the org used as a billing key + DO attribution tag: a
// non-empty, bounded string with no separator that the tag read-back (orgFromTag)
// or DO would misparse. Deliberately permissive on the exact charset (a real owner
// claim is already a DNS label); it exists to keep the meter attribution surface
// un-forgeable, not to re-validate IAM.
func validOrgSlug(org string) bool {
	org = strings.TrimSpace(org)
	if org == "" || len(org) > 128 {
		return false
	}
	return !strings.ContainsAny(org, ",: \t\r\n")
}

// NormalizeProject trims and validates a project scope read from the gateway-minted
// X-Project-Id header at the edge, returning the canonical project or "" (the
// org's default project) for an absent or invalid value. It is the ONE
// normalization the controllers apply, so every keyed surface downstream receives
// a project that already survives the tag/meter read-back. Built on the same
// validProjectSlug predicate the write boundaries use, so there is one project
// rule expressed once.
func NormalizeProject(project string) string {
	project = strings.TrimSpace(project)
	if !validProjectSlug(project) {
		return ""
	}
	return project
}

// validProjectSlug bounds the project exactly like the org, with one difference:
// the EMPTY project is valid — it is the org's default project. A non-empty
// project must survive the same tag/meter read-back (no "," / ":" / whitespace),
// so a caller can never smuggle a second attribution token through X-Project-Id.
func validProjectSlug(project string) bool {
	project = strings.TrimSpace(project)
	if project == "" {
		return true // default project
	}
	if len(project) > 128 {
		return false
	}
	return !strings.ContainsAny(project, ",: \t\r\n")
}
