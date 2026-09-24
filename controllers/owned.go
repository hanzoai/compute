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
	"encoding/json"
	"fmt"
	"strings"
)

// owned is the (owner, name) a write to an org-owned row acts on.
type owned struct {
	Owner string
	Name  string
}

// ownedWrite is the one rule for which row a write touches. The owner is the org
// the caller acts in, resolved as every org-scoped route resolves it (principal: a
// bearer's signed membership of the org it names, or the service caller's named
// org); the name is the one the address gives. A body may repeat them and may not
// differ, because the body is what the handler writes while the address is what
// authorization was asked about: a body naming another owner or another row asks
// to write where nobody checked. The requested org is the address's owner, else
// the body's; a bearer that is not a member of it is refused, so a row of the
// reserved admin org is written only by a member of admin (IsSuperAdmin).
//
// It returns the target, or a refusal to answer with. A create names its row in
// the body alone, so its name comes from there when the address gives none.
func (c *ApiController) ownedWrite(body []byte) (owned, string) {
	target, refusal := c.ownedTarget(body)
	if refusal == "" && target.Name == "" {
		return owned{}, "no name in this request"
	}
	return target, refusal
}

// ownedTarget is ownedWrite for a row whose name the caller may leave to the
// handler: the name is "" when neither the address nor the body gives one.
func (c *ApiController) ownedTarget(body []byte) (owned, string) {
	var claimed owned
	if err := json.Unmarshal(bodyOrEmpty(body), &claimed); err != nil {
		return owned{}, err.Error()
	}
	claimed.Owner, claimed.Name = strings.TrimSpace(claimed.Owner), strings.TrimSpace(claimed.Name)
	owner, name := c.Target()
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
	if owner != "" && claimed.Owner != "" && claimed.Owner != owner {
		return owned{}, fmt.Sprintf("the body names org %q; the address names %q", claimed.Owner, owner)
	}
	if name != "" && claimed.Name != "" && claimed.Name != name {
		return owned{}, fmt.Sprintf("the body names %q; the address names %q", claimed.Name, name)
	}
	if owner == "" {
		owner = claimed.Owner
	}
	if name == "" {
		name = claimed.Name
	}
	_, org := principal(c.Ctx.Header("Authorization"), owner)
	if org == "" {
		return owned{}, refuseNoOrg
	}
	if claimed.Owner != "" && claimed.Owner != org {
		return owned{}, fmt.Sprintf("the body names org %q; this request acts in %q", claimed.Owner, org)
	}
	return owned{Owner: org, Name: name}, ""
}

// bodyOrEmpty is a request body as JSON, with no body read as an empty object: a
// DELETE addressed by its path need send none.
func bodyOrEmpty(body []byte) []byte {
	if len(strings.TrimSpace(string(body))) == 0 {
		return []byte("{}")
	}
	return body
}
