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

package visor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/routers"
)

// bind serves a routed visor on its canonical socket inside a runtime dir owned
// by the test, and returns the app. Not Bootstrap: that opens the store and
// starts tickers, and none of that is what these tests are about.
func bind(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(zip.RuntimeDirEnv, t.TempDir())

	app := zip.New(zip.Config{AppName: Name, DisableStartupMessage: true})
	routers.Route(app)

	h, err := zip.Serve(app, zip.SocketPath(Name))
	if err != nil {
		t.Fatalf("serve %s on %s: %v", Name, zip.SocketPath(Name), err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return app
}

// bound waits for name's socket to actually be bound, and reports whether it
// got there. zip.Serve returns once the listeners have been STARTED, not once
// they are bound — binding happens on another goroutine and no transport reports
// it — so reading zip.Serving straight after Serve is a race, and one that
// passes on an idle machine and fails on a loaded one.
//
// It is worth knowing outside a test too: a caller that dials visor in the
// instant after it starts can legitimately get ErrNoPeer, so "no peer" means
// "not yet or not here", never "not deployed".
func bound(name string, within time.Duration) *zip.App {
	deadline := time.Now().Add(within)
	for {
		if app := zip.Serving(name); app != nil {
			return app
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

// TestPlaneResolvesByName is the fact that makes visor reachable to the rest of
// the fleet: bound to its canonical socket, it answers to its NAME.
//
// This is what visor did not do before. A sibling service reaches a peer through
// zip.SocketPath(name), so a process that binds no socket is not merely slow to
// reach — it does not exist, and the caller gets ErrNoPeer. That is why cloud
// still spoke plain HTTP to visor.hanzo.svc:19000 while every other hop had
// converted: there was nothing on the plane to convert TO.
func TestPlaneResolvesByName(t *testing.T) {
	app := bind(t)

	got := bound(Name, 5*time.Second)
	if got == nil {
		t.Fatalf("zip.Serving(%q) = nil — a caller would get ErrNoPeer", Name)
	}
	if got != app {
		t.Fatalf("zip.Serving(%q) resolved a different app", Name)
	}
}

// TestPlaneRejectsAnotherName is the control. Serving answers by NAME, so a test
// that only ever asks for the name it just bound cannot tell a real lookup from
// a function that returns the one app it knows about.
func TestPlaneRejectsAnotherName(t *testing.T) {
	bind(t)

	if got := bound("visor-not-this-one", 50*time.Millisecond); got != nil {
		t.Fatal("zip.Serving resolved a name nothing bound")
	}
}

// TestHereInvokesTheTypedOp proves the op is reachable BY NAME with no wire
// under it — the seam a co-resident caller uses instead of building a request
// for its own router to take apart again.
//
// The store is not open in a test, so the op answers its 503. That error IS the
// result: reaching it means zip found the op in the registry, ran it through the
// same validate → authorize → run core the socket and REST paths use, and handed
// back the handler's own error. An op that were merely an untyped route would
// not be in the registry at all and this would be "unknown op".
func TestHereInvokesTheTypedOp(t *testing.T) {
	app := bind(t)

	_, err := zip.Here[routers.Ping, routers.Health](context.Background(), app, "health", &routers.Ping{})
	if err == nil {
		t.Fatal("health answered ok with no store open")
	}
	if strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("zip.Here: %v — the op is not in the registry, so it projects nowhere", err)
	}
	if !strings.Contains(err.Error(), "store unreachable") {
		t.Fatalf("zip.Here: %v, want the handler's own answer", err)
	}
}
