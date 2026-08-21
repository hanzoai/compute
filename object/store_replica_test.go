// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/orm/relational"
)

// openTestOrgDB opens an org SQLite the same way baseStore.EngineFor does.
func openTestOrgDB(t *testing.T, path string) *relational.Engine {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	e, err := relational.NewEngine("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return e
}

// TestReplicatorHydrateShipRoundTrip proves the HA durability path: a "pod A"
// writes an org's rows and ships a snapshot to the object store; a fresh "pod B"
// (empty local disk) hydrates that org on open and sees the rows — SQLite survives
// a pod with no persistent volume.
func TestReplicatorHydrateShipRoundTrip(t *testing.T) {
	objDir := t.TempDir() // stands in for SeaweedFS
	t.Setenv("REPLICA_STORE", "file://"+objDir)

	// Pod A: write org rows, push a snapshot synchronously.
	rootA := t.TempDir()
	repA := newReplicator(rootA)
	if repA == nil {
		t.Fatal("newReplicator A returned nil with REPLICA_STORE set")
	}
	defer repA.close()

	pathA := DBPath(rootA, "acme")
	engA := openTestOrgDB(t, pathA)
	if _, err := engA.Exec(`CREATE TABLE t(x INT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := engA.Exec(`INSERT INTO t VALUES(7)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = engA.Close()
	if err := repA.pushNow("acme", pathA); err != nil {
		t.Fatalf("pushNow: %v", err)
	}

	// Pod B: fresh disk, hydrate must pull acme's DB and expose the row.
	rootB := t.TempDir()
	repB := newReplicator(rootB)
	defer repB.close()
	pathB := DBPath(rootB, "acme")
	if err := os.MkdirAll(filepath.Dir(pathB), 0o700); err != nil {
		t.Fatalf("mkdir B: %v", err)
	}
	if err := repB.hydrate("acme", pathB); err != nil {
		t.Fatalf("hydrate B: %v", err)
	}
	engB := openTestOrgDB(t, pathB)
	defer engB.Close()
	var got int
	if _, err := engB.SQL(`SELECT x FROM t`).Get(&got); err != nil {
		t.Fatalf("read hydrated: %v", err)
	}
	if got != 7 {
		t.Fatalf("hydrated x = %d, want 7 (cross-pod durability failed)", got)
	}
}

// TestReplicatorDisabledIsLocalOnly proves the absence of REPLICA_STORE yields a
// nil replicator whose methods are safe no-ops (today's local-only behavior).
func TestReplicatorDisabledIsLocalOnly(t *testing.T) {
	t.Setenv("REPLICA_STORE", "")
	rep := newReplicator(t.TempDir())
	if rep != nil {
		t.Fatal("replicator must be nil when REPLICA_STORE is unset")
	}
	// Nil-safe: hydrate/ship/pushNow/close are no-ops.
	if err := rep.hydrate("acme", "/tmp/x.db"); err != nil {
		t.Fatalf("nil hydrate: %v", err)
	}
	if err := rep.pushNow("acme", "/tmp/x.db"); err != nil {
		t.Fatalf("nil pushNow: %v", err)
	}
	rep.ship("acme", "/tmp/x.db")
	rep.close()
}
