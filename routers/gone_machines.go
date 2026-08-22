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

// goneMachines is the machines family's share of the retirement table: an
// address that moved, and the address that replaced it.
//
// The launch VERB left the path. A machine is created in the collection it
// joins, so POST /v1/machines is the create and /v1/machines/launch was a second
// door onto the same one.
//
// The six /v1/*-machine addresses are NOT here, because they do not answer for
// this collection and no rename can make them. They read the visor `machine`
// table, which GetMachines and GetMachine rebuild on every read
// (object.SyncMachinesCloud deletes every row for the owner and re-inserts what
// the org's OWN provider credentials list); /v1/machines reads live droplets on
// the house account by the hanzo-org tag. The two disagree on all three things a
// caller depends on: the tenant (?owner verbatim against the token's Owner
// claim), the item key (`owner/name` against a droplet id, which
// service.GetOrgMachine parses with strconv.Atoi), and the row shape. Carrying
// those addresses onto /v1/machines would answer from the other store for the
// other tenant at 200, and cloud unions the two deliberately (apps/visor
// managedMachines). Nor can /v1/launch-machine fold into POST /v1/machines:
// it provisions on a NAMED provider out of the org's own credentials and is not
// metered, while the collection's create spends on the house account through
// commerce — which account pays is not something to settle with a query
// parameter on one POST.
var goneMachines = map[string]string{
	"/v1/machines/launch": "/v1/machines",
}
