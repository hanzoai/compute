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

package object

import "xorm.io/xorm"

// pgStore serves every owner from one shared engine -- the historical visor
// backend. owner is ignored: tenant isolation is a WHERE owner=? clause, not a
// separate database. This is the default provider and keeps the live path
// byte-identical to before the Base seam existed.
type pgStore struct {
	engine *xorm.Engine
}

func (s *pgStore) EngineFor(string) (*xorm.Engine, error) { return s.engine, nil }

// Shared is the same one engine -- the shared tables are plain rows in it.
func (s *pgStore) Shared() *xorm.Engine { return s.engine }

// PullSharedLeases / PushShared are no-ops under Postgres: the shared engine already
// IS the single, durable, linearizable coordination store, so the insert-once lease PK
// holds cluster-wide with no hydrate/ship. (These exist only so the Base backend can
// make its pod-local `_global` SQLite coord cluster-safe across a leadership handoff.)
func (s *pgStore) PullSharedLeases() error { return nil }
func (s *pgStore) PushShared() error       { return nil }

// AllEngines is the single engine: it already holds every org's rows, so a
// cross-org sweep is one query against it.
func (s *pgStore) AllEngines() ([]*xorm.Engine, error) { return []*xorm.Engine{s.engine}, nil }

func (s *pgStore) Close() error { return s.engine.Close() }
