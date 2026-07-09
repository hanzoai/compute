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
// the live pod set and gate writes with ha.IsOwner (github.com/hanzoai/ha) — the
// election primitive already does the HRW election; no code here changes.

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/vfs/pkg/backend"
	_ "github.com/hanzoai/vfs/pkg/backend/file" // register file:// (dev/test)
	_ "github.com/hanzoai/vfs/pkg/backend/s3"   // register s3:// (Hanzo S3, prod)
	"github.com/hanzoai/vfs/replica"
	"xorm.io/xorm"
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

// mergeSharedLeases pulls the object store's committed billing-lease rows for owner
// and inserts-if-absent each into the LIVE coord engine — guarantee (2) of exactly-once
// (durable lease history across a leadership handoff). It never swaps the coord file
// (that would need the engine closed and strand the cached Shared() pointer): it reads
// the remote snapshot through a TRANSIENT read engine and replays only the lease rows,
// so the merge is safe while the coord engine stays open for process life.
//
// The merge is idempotent: an already-present PK is a no-op (the insert-once
// constraint), so a lease a prior owner claimed is present locally BEFORE this replica
// claims — its own claim of the same hour/unit then fails the PK and it correctly skips.
//
// Nil replicator (local-only, single writer) or a missing/empty remote (no owner has
// shipped yet) is not an error — there is nothing to merge and the local coord is
// already authoritative.
func (r *replicator) mergeSharedLeases(owner string, coord *xorm.Engine) error {
	if r == nil {
		return nil
	}
	data, _, err := r.store.Get(r.ctx, replica.DBPath(owner, "", "visor"))
	if err != nil || len(data) == 0 {
		return nil // no remote yet — nothing to merge.
	}
	// Materialize the remote snapshot in a throwaway temp dir and open a transient read
	// engine on it. Dir + engine are removed before we return; nothing persists.
	dir, err := os.MkdirTemp("", "visor-lease-merge")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, owner+".db")
	if err := replica.RestoreFile(tmp, data); err != nil {
		return err
	}
	remote, err := xorm.NewEngine("sqlite", tmp+sqlitePragmas)
	if err != nil {
		return err
	}
	defer remote.Close()

	return mergeLeaseRows(remote, coord)
}

// mergeLeaseRows copies every billing-lease row present in src but absent in dst,
// inserting-if-absent so an existing PK is a no-op. It covers exactly the tables whose
// insert-once claim guards money: MeterLease (hourly compute sweep), BillingLease
// (daily/monthly fleet units), and CostCursor (the BYOC watermark advanced under the
// BillingLease). One error aborts the merge so the caller fails CLOSED (a claim that
// cannot confirm it has the prior owner's rows must not proceed).
func mergeLeaseRows(src, dst *xorm.Engine) error {
	var meters []MeterLease
	if err := src.Find(&meters); err != nil {
		return err
	}
	for i := range meters {
		if _, err := dst.Insert(&meters[i]); err != nil {
			// A duplicate-PK insert is reported by xorm as an error on some drivers;
			// treat "already present" as success by checking existence, so a real
			// error still aborts.
			if exists, e := dst.Exist(&MeterLease{Hour: meters[i].Hour}); e != nil {
				return e
			} else if !exists {
				return err
			}
		}
	}

	var units []BillingLease
	if err := src.Find(&units); err != nil {
		return err
	}
	for i := range units {
		if _, err := dst.Insert(&units[i]); err != nil {
			if exists, e := dst.Exist(&BillingLease{Unit: units[i].Unit}); e != nil {
				return e
			} else if !exists {
				return err
			}
		}
	}

	var cursors []CostCursor
	if err := src.Find(&cursors); err != nil {
		return err
	}
	for i := range cursors {
		c := cursors[i]
		exists, err := dst.Exist(&CostCursor{Owner: c.Owner, Provider: c.Provider, Month: c.Month})
		if err != nil {
			return err
		}
		if exists {
			continue // never REGRESS a watermark: the local value is at least as advanced.
		}
		if _, err := dst.Insert(&c); err != nil {
			return err
		}
	}
	return nil
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
