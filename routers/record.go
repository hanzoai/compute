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

package routers

import (
	"fmt"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/util"
)

const recordUserIDKey = "recordUserId"

// RecordMessage is the audit-record middleware. Beego split this into a
// BeforeRouter hook (stash the acting user) and an AfterExec hook (build+persist
// the record from the response); ZAP composes both around one c.Next() — the
// before-work stashes the user id, the handler runs, then the after-work reads
// the response envelope the handler stashed and writes the audit record. Login,
// signup and get-assets are exempted from the user-id stash exactly as before.
func RecordMessage(c *zip.Ctx) error {
	path := c.Path()
	if path != "/v1/login" && path != "/v1/signup" && path != "/v1/get-assets" {
		if userId := getUsername(c); userId != "" {
			c.Locals(recordUserIDKey, userId)
		}
	}

	err := c.Next()

	afterRecordMessage(c)
	return err
}

func afterRecordMessage(c *zip.Ctx) {
	record, err := object.NewRecord(c, c.Locals(object.RecordResponseKey))
	if err != nil {
		fmt.Printf("AfterRecordMessage() error: %s\n", err.Error())
		return
	}

	userId, _ := c.Locals(recordUserIDKey).(string)
	if userId != "" {
		record.Organization, record.User = util.GetOwnerAndNameFromId(userId)
	}

	object.AddRecord(record)
}
