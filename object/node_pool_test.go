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

package object

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/digitalocean/godo"
)

type fakeNodePoolDeleter struct {
	err    error
	called bool
}

func (f *fakeNodePoolDeleter) DeleteNodePool(poolID string) error {
	f.called = true
	return f.err
}

// doErrorResponse builds an error identical in shape to what godo returns for a
// non-2xx DO API response, so IsNotFound is exercised against the real type.
func doErrorResponse(status int) error {
	return &godo.ErrorResponse{
		Response: &http.Response{StatusCode: status},
		Message:  fmt.Sprintf("digitalocean returned %d", status),
	}
}

// TestConfirmCloudPoolDeleted pins the delete-flow decision: a successful or
// already-gone (404) provider response confirms deletion, while a transient 422
// (still provisioning) or any other error is propagated so the caller retries
// and the DB row is preserved.
func TestConfirmCloudPoolDeleted(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"provider deletes ok", nil, false},
		{"provider 404 already gone", doErrorResponse(http.StatusNotFound), false},
		{"wrapped 404 already gone", fmt.Errorf("failed to delete node pool p: %w", doErrorResponse(http.StatusNotFound)), false},
		{"422 still provisioning retries", doErrorResponse(http.StatusUnprocessableEntity), true},
		{"wrapped 422 still provisioning retries", fmt.Errorf("failed to delete node pool p: %w", doErrorResponse(http.StatusUnprocessableEntity)), true},
		{"500 propagates", doErrorResponse(http.StatusInternalServerError), true},
		{"opaque error propagates", errors.New("connection refused"), true},
	}
	for _, tc := range cases {
		client := &fakeNodePoolDeleter{err: tc.err}
		err := confirmCloudPoolDeleted(client, "pool-123")
		if !client.called {
			t.Errorf("%s: expected DeleteNodePool to be invoked on the provider", tc.name)
		}
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: confirmCloudPoolDeleted err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
