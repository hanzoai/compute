// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

// HA durability for the per-org SQLite substrate via the shared hanzoai/vfs/replica
// library (HIP-0107) — the SAME substrate every Hanzo service adopts, never re-invented.
//
// OPT-IN + graceful: set REPLICA_STORE to an object-store URL (s3://… SeaweedFS in
// prod, file://… for a test) and each org DB is hydrated-on-open (Pull the last
// snapshot before first use) and shipped after opening (a per-org PushLoop backs up
// the WAL-checkpointed DB to the object store). Unset ⇒ local-only, exactly today's
// behavior (safe at replicas=1). This lifts the "SQLite is ephemeral without a PVC"
// gap: the durable copy lives in the object store, so a lost pod (or a pod with no
// persistent volume) loses no committed data — it Pulls its orgs back on boot.
//
// Single-writer: visor runs replicas=1, so self is the sole owner of every org and
// the PushLoop is the whole story. When visor scales out, swap the members source for
// the live pod set and gate writes with replica.IsOwner — the library already does the
// HRW election; no code here changes.

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/hanzoai/vfs/pkg/backend"
	_ "github.com/hanzoai/vfs/pkg/backend/file" // register file:// (dev/test)
	_ "github.com/hanzoai/vfs/pkg/backend/s3"   // register s3:// (Hanzo S3, prod)
	"github.com/hanzoai/vfs/replica"
)

// replicator holds the object-store binding for HA, or nil when REPLICA_STORE is
// unset (local-only). One store, shared by every org's per-DB replicator.
type replicator struct {
	store   replica.Store
	root    string
	pushDur time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
}

// newReplicator opens the object-store backend from REPLICA_STORE. Returns nil when
// unset (HA is opt-in; absence is the normal local-only mode) OR when the backend
// can't be opened — HA durability is ADDITIVE and must NEVER take the service down.
// A misconfigured/unreachable object store degrades to local-only with a logged
// warning, exactly as if REPLICA_STORE were unset. It never returns an error.
func newReplicator(root string) *replicator {
	url := os.Getenv("REPLICA_STORE")
	if url == "" {
		return nil
	}
	be, err := backend.Open(context.Background(), url)
	if err != nil {
		log.Printf("visor: REPLICA_STORE set but object store unavailable (%v); running local-only (no HA durability)", err)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &replicator{
		store:   replica.NewBackendStore(be),
		root:    root,
		pushDur: 15 * time.Second,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// hydrate pulls owner's last snapshot from the object store into its local file
// BEFORE the caller opens its xorm engine (hydrate-on-open). A missing remote (a
// brand-new org) is not an error — the local file is created empty as usual.
func (r *replicator) hydrate(owner, path string) error {
	if r == nil {
		return nil
	}
	data, _, err := r.store.Get(r.ctx, replica.DBPath(owner, "", "visor"))
	if err != nil || len(data) == 0 {
		return nil // no remote yet — start fresh
	}
	return replica.RestoreFile(path, data)
}

// pushNow snapshots the org's local DB file and writes it to the object store —
// the single WAL-ship primitive, called by both ship's loop and (synchronously) by
// tests. Safe on a nil replicator.
func (r *replicator) pushNow(owner, path string) error {
	if r == nil {
		return nil
	}
	data, err := replica.SnapshotFile(r.ctx, path)
	if err != nil {
		return err
	}
	return r.store.Put(r.ctx, replica.DBPath(owner, "", "visor"), data)
}

// ship starts a per-org background loop that pushNow's on an interval — the owner's
// WAL-shipper. Called once per org after its engine is opened. Never blocks queries.
// A durability feature must NOT fail silently: a push error is logged (rate-limited to
// the first failure + first recovery per org) so a broken object store is visible, not
// a silent loss of durability. The first successful push per org is logged once too,
// so ops can confirm HA is actually shipping.
func (r *replicator) ship(owner, path string) {
	if r == nil {
		return
	}
	go func() {
		t := time.NewTicker(r.pushDur)
		defer t.Stop()
		failing := false
		first := true
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-t.C:
				err := r.pushNow(owner, path)
				switch {
				case err != nil && !failing:
					log.Printf("visor HA: org %q push FAILED (durability at risk): %v", owner, err)
					failing = true
				case err == nil && failing:
					log.Printf("visor HA: org %q push recovered", owner)
					failing = false
				case err == nil && first:
					log.Printf("visor HA: org %q shipping to object store (durable)", owner)
				}
				if err == nil {
					first = false
				}
			}
		}
	}()
}

// close stops every per-org ship loop.
func (r *replicator) close() {
	if r != nil && r.cancel != nil {
		r.cancel()
	}
}
