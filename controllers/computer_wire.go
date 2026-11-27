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
	"context"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/util"
)

// RawComputerForMachine is the response writer for the one typed op whose
// answer is not JSON. zip marshals a typed Out with c.JSON, which also forces
// Content-Type to application/json — both wrong when hanzod answered with a
// PNG. The typed handler stashes the upstream answer in a request-scoped slot;
// after the route returns, this filter replaces status, Content-Type and body
// with those exact bytes. The op stays in the registry (OpenAPI, MCP, CLI,
// call plane), and the wire stays verbatim on REST.
//
// It applies ONLY to the machine computer address, matched by shape: a suffix
// match would also catch a longer, unrelated route. It is installed on the
// app's filter chain rather than on a `zip.Post(app.With(...))` wrapper so
// zipdoc still sees a typed registration made directly on the *zip.App and can
// file its doc comment under the right path.
//
// When the typed handler refuses, the slot is empty and zip's own problem
// response is left alone.
func RawComputerForMachine(c *zip.Ctx) error {
	if c.Method() != http.MethodPost || !util.IsMachineComputerPath(c.Path()) {
		return c.Next()
	}
	slot := &ComputerResponse{}
	// fiber keeps the request's context.Context in a user-value slot, and zip
	// derives the typed handler's ctx from it. Put the slot on THAT context so
	// ctx.Value in Computer reaches the same value.
	c.Fiber().SetContext(context.WithValue(c.Fiber().Context(), ComputerResponseKey{}, slot))

	err := c.Next()
	if err != nil || !slot.Raw {
		return err
	}
	c.Fiber().Response().Header.SetContentType(slot.ContentType)
	c.Status(slot.Status)
	return c.Fiber().Send(slot.Body)
}
