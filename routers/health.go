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

package routers

import (
	"context"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
)

// Ping is what a probe sends: nothing. A health check asks one question and
// carries no arguments to ask it with, and saying so as a type is what lets the
// op be declared like every other — one shape, no special case.
type Ping struct{}

// Health is compute's answer to a probe.
//
// Status is "ok" or the response is not a 200 at all, so a reader never has to
// parse the body to learn the verdict — which matters because the reader is
// kubelet, and kubelet reads the status code and nothing else. Backend names the
// store that was reached, so a human looking at a curl can tell a Base pod from
// a Postgres one without consulting the deployment.
type Health struct {
	Status  string `json:"status"`
	Backend string `json:"backend"`
}

// health answers whether compute can do its job, which reduces to whether it can
// reach its store: every route on this service reads or writes one.
//
// The failure is a 503 and not a 200-with-a-sad-field. A readiness probe that
// always answers 200 cannot take a broken pod out of the Service, and that is
// precisely the bug this op was written to end — compute's probes used to request
// /api/health, which is not a route, so TransparentStatic served the SPA's
// index.html and every probe passed with the database face down.
func health(_ context.Context, _ *Ping) (*Health, error) {
	if err := object.Ready(); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "compute: store unreachable: %v", err)
	}
	return &Health{Status: "ok", Backend: string(object.ConfiguredBackend())}, nil
}
