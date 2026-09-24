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

// owned is the (owner, name) of an org-owned row.
type owned struct {
	Owner string
	Name  string
}

// ownedWrite returns the row a write acts on: the caller's resolved org and the
// address's name (the body's, for a create). A body naming another org is refused.
func (c *ApiController) ownedWrite(body []byte) (owned, string) {
	target, refusal := c.ownedTarget(body)
	if refusal == "" && target.Name == "" {
		return owned{}, "no name in this request"
	}
	return target, refusal
}

// ownedTarget is ownedWrite with the name left "" when nothing gives one.
func (c *ApiController) ownedTarget(body []byte) (owned, string) {
	var claimed owned
	if err := json.Unmarshal(bodyOrEmpty(body), &claimed); err != nil {
		return owned{}, err.Error()
	}
	claimed.Owner, claimed.Name = strings.TrimSpace(claimed.Owner), strings.TrimSpace(claimed.Name)
	owner, name := c.Target()
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
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

// bodyOrEmpty reads an empty body as {}.
func bodyOrEmpty(body []byte) []byte {
	if len(strings.TrimSpace(string(body))) == 0 {
		return []byte("{}")
	}
	return body
}
