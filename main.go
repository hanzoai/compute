// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

// Command visor is the Hanzo Visor cloud compute/VM/GPU management service:
// zip-native on the hanzoai/orm store. It serves two addresses — the HTTP edge
// the ingress reaches, and the canonical ZAP socket the rest of the fleet dials
// it by name on.
//
// Run it with no arguments to serve (the container contract); `visor version`
// prints the build version.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hanzoai/ha"
	"github.com/spf13/cobra"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/conf"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/pkg/visor"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "(dev)"

func main() {
	// One context rooted at the process signals, so SIGINT/SIGTERM drives a
	// graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var zapAddr, httpAddr string
	var createDatabase bool

	// The root command IS the server — the container runs a bare `/visor`
	// (optionally `--createDatabase=true`), so serve is the default action.
	root := &cobra.Command{
		Use:           "visor",
		Short:         "Hanzo Visor — cloud compute/VM/GPU management service (zip-native)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), zapAddr, httpAddr)
		},
	}
	f := root.Flags()
	f.StringVar(&zapAddr, "zap", zip.SocketPath(visor.Name), "ZAP listen address (empty = HTTP edge only)")
	f.StringVar(&httpAddr, "http", defaultHTTPAddr(), "HTTP edge listen address")
	// Accepted for container-command compatibility; the store creates its schema
	// on open (object.InitAdapter) regardless, so this is informational.
	f.BoolVar(&createDatabase, "createDatabase", false, "create the database schema on boot (always applied)")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the visor build version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "visor %s\n", version)
		},
	})

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "visor: %v\n", err)
		os.Exit(1)
	}
}

// serve boots visor and binds the listener(s). visor.Bootstrap is the single
// in-process boot path — DB, authz, parsers, filters, routes and background
// tickers — shared verbatim with the embedded cloud mount, so standalone and
// fused never drift. Here main() owns the listener.
//
// TWO addresses, and the ZAP one is not optional in practice. The HTTP edge is
// what a browser and the ingress reach; the socket is how the rest of the fleet
// reaches visor BY NAME, and a peer that binds no socket does not exist to a
// caller — zip resolves a peer through zip.SocketPath, so the failure is
// ErrNoPeer rather than a connection refused anyone would recognise. Defaulting
// the socket ON is what makes `visor` a name a sibling service can dial; an
// operator who genuinely wants an edge-only process passes --zap="".
func serve(ctx context.Context, zapAddr, httpAddr string) error {
	// The standalone Deployment runs replicas: 1 (universe
	// charts/app/values/hanzo/visor.yaml), so this process is the only writer and
	// correctly elects itself. Declared HERE and not in visor.Bootstrap on
	// purpose: Bootstrap is shared verbatim with the embedded cloud mount, whose
	// replica count is cloud's, not ours. Registering it there would hand a
	// multi-replica cloud the claim "I am alone" and double-debit every hour.
	//
	// This is the one topology assertion in the binary, and it is only true while
	// that values file says 1. Raising replicas REQUIRES replacing this with a
	// real membership source first — see object/coordinator.go.
	object.RegisterMembership(ha.Static(object.SelfID()))

	// Which cloud accounts this deployment may spend on: the active cloud
	// Providers owned by platformOwner. Unset registers nothing and service
	// falls back to the single configured DigitalOcean token, which is what a
	// deployment that has not been told where its accounts live did before.
	// Seal provider secrets in KMS when the deployment is wired for it (VISOR_KMS_*):
	// the vault is unlocked once here, so credential reads below resolve from KMS
	// instead of the plaintext column. No KMS env keeps the plaintext path.
	object.ConfigureKMS(strings.TrimSpace(conf.GetConfigString("platformOwner")))

	object.RegisterCloudCredentials(strings.TrimSpace(conf.GetConfigString("platformOwner")))

	// And how those accounts are reached: through hanzoai/egress when it is
	// configured, so the cloud keys are not in this process at all.
	if err := carry(); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	app := visor.Bootstrap()

	// Translate ctx cancellation (SIGINT/SIGTERM) into a graceful shutdown.
	go func() {
		<-ctx.Done()
		_ = app.Shutdown()
	}()

	addrs := make([]string, 0, 2)
	if zapAddr != "" {
		addrs = append(addrs, zapAddr)
	}
	addrs = append(addrs, httpAddr)

	if err := app.Listen(addrs...); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// defaultHTTPAddr honours the historical httpport (app.conf / env), defaulting
// to :19000 — the port the container EXPOSEs and the compose maps.
func defaultHTTPAddr() string {
	port := conf.GetConfigString("httpport")
	if port == "" {
		port = "19000"
	}
	return "http://:" + port
}
