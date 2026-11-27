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

package object

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestComputerBodyIsNeverAudited is the logging half of the computer tunnel's
// contract. The audit filter records c.Body() in Record.Object by default; on
// this one address that body carries typed text, screenshots and possibly a
// credential, so it must be redacted at the recorder, not left to a caller to
// remember. A neighbouring address is the control: it proves the recorder
// still captures bodies where that is safe.
func TestComputerBodyIsNeverAudited(t *testing.T) {
	const secret = "SECRET_TYPED_TEXT_9f3c"
	for _, tc := range []struct {
		path string
		want bool // whether the body appears in the audit row
	}{
		{"/v1/machines/acme/mac-1/computer", false},
		{"/v1/machines/acme/mac-1/agent", true},
		{"/v1/machines/acme/computer", true}, // wrong shape, no redaction
	} {
		t.Run(tc.path, func(t *testing.T) {
			var got *Record
			app := zip.New(zip.Config{})
			// Middleware goes on BEFORE the routes: a Use registered later does
			// not retroactively wrap addresses fiber already holds.
			app.Use(zip.H(func(c *zip.Ctx) error {
				err := c.Next()
				r, e := NewRecord(c, nil)
				if e != nil {
					t.Errorf("NewRecord: %v", e)
				} else {
					got = r
				}
				return err
			}))
			app.Post("/v1/machines/:owner/:name/computer", func(c *zip.Ctx) error { return nil })
			app.Post("/v1/machines/:owner/:name/agent", func(c *zip.Ctx) error { return nil })
			app.Post("/v1/machines/:owner/computer", func(c *zip.Ctx) error { return nil })

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"approval":true,"text":"`+secret+`"}`))
			if _, err := app.Fiber().Test(req); err != nil {
				t.Fatalf("POST %s: %v", tc.path, err)
			}
			if got == nil {
				t.Fatalf("POST %s produced no audit record", tc.path)
			}
			leaked := strings.Contains(got.Object, secret)
			if leaked != tc.want {
				t.Fatalf("audit Object = %q, body leaked = %v, want %v", got.Object, leaked, tc.want)
			}
		})
	}
}
