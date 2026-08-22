# Visor

## Cloud consolidation status (HIP-0106)
Visor's compute REST surface — `/v1/machines`, `/v1/gpus`, `/v1/clusters` — is
already served **natively** by the unified `cloud` binary (`hanzoai/cloud`
`clients/visor`, org-scoped via `principal`); it currently PROXIES this
standalone service for data. Visor stays standalone (the active compute backend)
until the native port lands: replacing the XORM `object/*` persistence with a
Base store and reusing the pure-`godo` `service/{digitalocean,doks}.go` on top of
cloud's `clients/do` — a bounded consolidation wave sequenced in
`hanzoai/cloud → docs/consolidation.md`. This is NOT a deprecation; do not
retire this repo until that wave merges. (`hanzoai/visor-legacy` is the archived
pre-fork repo — that one IS retired.)

## Overview
Visor is Hanzo's multi-provider cloud VM management platform. It provisions, monitors, and manages virtual machines across Hetzner, AWS Lightsail, and DigitalOcean (plus Azure, GCP, Aliyun, Proxmox, VMware, KVM). Originally forked from Casvisor, fully rebranded to `github.com/hanzoai/visor`.

## Architecture

### Core Layers
- **Controllers** (`/controllers/`): HTTP handlers, JWT auth
- **Service** (`/service/`): Provider adapters implementing `MachineClientInterface`
- **Object** (`/object/`): Data models, DB operations (`hanzoai/orm`), plan seeds
- **Billing** (`/billing/`): Pricing engine
- **AuthZ** (`/authz/`): hanzoai/authz-based authorization

### One framework: zip
Visor serves on `github.com/zap-proto/zip` and nothing else. Beego is gone —
no import, no `go.mod` require, no prose describing the current design. What
keeps it gone is `deps_test.go`, whose `banned` map fails the build on any
`go.mod` naming beego (either import path), gin, echo, chi, mux, iris, buffalo,
revel, martini or negroni. It matches on a path boundary, so `…/beego/v2` and
`…/echo/v4` are caught — the only shape that actually occurs.

`zap-proto/fiber` + `valyala/fasthttp` are NOT banned: they are zip's own
engine, not a second framework. What matters is that visor does not reach PAST
zip to them. Two places still do, both named:
- `controllers/tunnel.go` builds a `websocket.FastHTTPUpgrader` by hand. It
  cannot use `wsx.Upgrade`, whose handler is `func(*wsx.Conn) error` and so has
  no `Ctx` — and the guacamole tunnel MUST read its query params BEFORE the
  hijack, because the fiber ctx is pooled and recycled after the handler
  returns. Lifting this needs a wsx that hands the handler its Ctx.
- `pkg/visor/embed.go` adapts the fiber app to an `http.Handler` so cloud can
  mount visor in-process. The ZAP plane is the replacement; it goes when cloud
  switches to calling by name.

Everything else that touched `fasthttp/websocket` now imports
`github.com/zap-proto/zip/wsx`, whose `Conn` is a type ALIAS for the same type,
so it was an import swap and no type churn.

### The ZAP plane, and the typed op
`main.go --zap` defaults to `zip.SocketPath(visor.Name)`, so visor binds its
canonical socket alongside the HTTP edge and `zip.Serving("visor")` resolves.
This is what a sibling reaching visor BY NAME needs: a peer that binds no socket
does not exist to a caller, and the error is `ErrNoPeer`. `--zap=""` opts out.

Binding is ASYNCHRONOUS — `zip.Serve` returns once listeners are started, not
bound — so a caller dialling in the instant after start can legitimately get
`ErrNoPeer`. It means "not yet, or not here", never "not deployed".

`/v1/health` is visor's first TYPED op (`zip.Get[Ping, Health]`, `routers/health.go`),
which is what puts visor in the op registry every projection reads: OpenAPI,
MCP, CLI, SDK, and the by-name call plane. `zip.Here` reaches it in-process with
no wire. A machine's AGENT is the second noun to land typed (4 ops,
`controllers/agent_binding.go`, registered by `routers.registerAgent`). The
remaining 68 routes are still untyped controller methods, so they project
nowhere — see "Typed ops: what is left".

Two defects it fixed, both silent:
1. The k8s probes requested `/api/health`, which is not a route. Being outside
   `/v1/`, it fell to `TransparentStatic`, which serves `index.html` as a **200**
   — so liveness and readiness measured the static file server and could not
   fail while the process could open a file. They now request `/v1/health`,
   which answers **503** when the store is unreachable.
2. `TransparentStatic` also shadowed zip's own control plane
   (`/.well-known/openapi.json`, `/docs`, `/mcp`) for the same reason: its rule
   was "not `/v1/` ⇒ it is a file". With a web build present those answered 200
   with HTML to clients parsing JSON. Static now yields to them (`control` in
   `routers/filter.go`); `MCPPath` is stated once there and handed to
   `zip.Config` so the path let through and the path mounted cannot drift.

Health is registered AHEAD of the filter chain, deliberately. A probe carries no
credentials, so `ApiFilter` would put it to the policy engine as "anonymous" —
and `authz.IsAllowed` panics on a nil Enforcer, which is a 500. An authz problem
would then read to kubelet as every pod being sick. `RecordMessage` is the other
half: it writes an audit row per request, and two probes every ten seconds is
~17k rows/day describing nothing.

### There is ONE ORM
`hanzoai/orm` is the ORM. `github.com/hanzoai/xorm` appears in `go.mod` only as
an INDIRECT dependency reached through it (`go mod why`:
`visor/object → hanzoai/orm/relational → hanzoai/xorm`) — visor imports it
nowhere, and `relational.Engine` is a type ALIAS for `xorm.Engine`.

The `xorm:"varchar(100)"` struct tags on the models are NOT a second ORM and
must not be renamed: `hanzoai/orm` reads that exact tag
(`orm@v0.6.20/engine/schema.go:106` — `field.Tag.Get("xorm")`). Renaming them
breaks the schema mapping. The migration is done; the tag name is just the
tag name.

### Typed ops: what is left (IN PROGRESS, noun by noun)
68 of visor's 73 routes are still untyped `func (c *ApiController) X()` methods
registered through `h()`. They serve fine and project NOWHERE — no OpenAPI
schema, no MCP tool, no CLI verb, no `zip.Here`.

Two nouns have landed: `/v1/health`, and a machine's AGENT — four ops in
`controllers/agent_binding.go`, registered by `routers.registerAgent`, at ONE
address with the method carrying the verb:

| Method | Path | Was |
|--------|------|-----|
| GET | `/v1/machines/agents` | `GET /v1/agent-bindings` |
| PUT | `/v1/machines/:id/agent` | `POST /v1/machines/:id/bind-agent` |
| GET | `/v1/machines/:id/agent` | `GET /v1/machines/:id/agent-binding` |
| DELETE | `/v1/machines/:id/agent` | `DELETE /v1/machines/:id/agent-binding` |

Those four DROPPED the envelope, exactly as the order below requires: the answer
is the value, the status is the outcome (absent read → 404, unbind → 204), and
the org-scoped list is `{"agentBindings":[…]}`. It landed with its cloud caller
in the same change (`hanzoai/cloud` `apps/visor/{client,bots,visor}.go`), which
is what makes the break safe. `controllers/agent_wire_test.go` pins those
answers; the fake vm in cloud's `apps/visor/bots_http_test.go` is written to
match it.

An op declares the identity it reads — `header:"Authorization"` plus
`url:"owner"` on the embedded `caller` — because a typed handler is given a
`context.Context` and no request to reach into. `principal()` is the ONE rule
resolving those two into an org, and `resolveComputeOrg` now calls it rather
than restating it.

Converting them is not a mechanical rewrite, and the reason is the wire, not the
handlers. Visor answers the casibase envelope — HTTP 200 with
`{status:"ok"|"error", msg, data}`, where a logical failure is a 200 — and
`hanzoai/cloud`'s `apps/visor/client.go` unwraps exactly that on ~18 endpoints
(`bots.go`, `k8s.go`, `visor.go`). A typed op returns its `Out` directly and
signals failure with a status code, so every converted route is a WIRE BREAK
that must land in the same change as its cloud caller. Fixing it forward is the
right call — there are no external clients — but it is a two-repo change, and a
half-converted visor has two handler styles and two error shapes, which is worse
than either.

Order to do it in:
1. Convert cloud's `client.go` off HTTP onto the plane (`zip.Ask`/`zip.Call` by
   name) — the socket it needs now exists. No visor change required. NOT DONE.
2. Convert visor's routes to typed ops noun by noun (machines, k8s, node-pools,
   volumes, plans), each with its cloud caller, dropping the envelope as each
   lands. Started: agent DONE.
3. `pkg/visor/embed.go`'s fiber→`http.Handler` adaptor goes when step 1 lands.

Step 2 before step 1 costs one thing, and it is paid: while the migration runs,
cloud's client reads TWO upstream wires and has to be told which — `cl.call` for
the enveloped legacy ops (23 call sites) and `cl.op` for the typed ones (9).
They share one request half, and `call` disappears when the last noun lands.

Route/contract bookkeeping: `routers/router_contract_test.go` pins the surface
at 73 (GET 32, POST 37, DELETE 3, PUT 1) and reads the LIVE fiber router, so a
typed op swapped in for an untyped route keeps the same contract line and the
test keeps holding. The typed rows name package functions rather than
`ApiController` methods.

A RETIRED address is the one served route that is deliberately outside that
contract. `registerGone` mounts it on `zip.Undeclared`, so the router carries it
and `App.Declaration` does not — and the contract test tells the two apart with
`App.Declares`, the same question every projection asks, rather than a second
list of what to skip. It matters because a retired address answers EVERY method
(410 is about the target resource, and a caller who also got the verb wrong still
needs the successor): published, that is one dead operation per method per
address in the surface customers read. It is registered AHEAD of the filter
chain, beside health — no credential admits a resource that does not exist, and
behind the policy engine a stale caller is told 403 and never learns where the
resource went.

One piece of folklore worth not repeating: registering a literal ahead of a
`:param` sibling does NOT decide the match. Fiber prefers the static segment
whatever the order — measured by moving `registerAgent` below the `:id` routes
and watching `/v1/machines/agents` still reach `ListAgents`. What is pinned is
the outcome (`routers.TestAgentsIsNotAMachineId`), not an ordering rule that is
not one.

### Storage backends (Postgres -> Base, additive)
Visor persists 10 XORM tables (Asset, Provider, Machine, Record, Session,
NodePool, Plan, Volume, AgentBinding, MeterLease), all keyed by `Owner` (=org).
Historically one shared Postgres DB (`hanzo_visor`).

Per Hanzo's storage rule and the tenant-data-hierarchy HIP, visor is moving onto
hanzoai/base per-org SQLite. The seam is one interface -- `engineProvider` in
`object/store.go` -- resolving the `*xorm.Engine` that serves an owner:
- `pgStore` (default): one shared engine for every owner (WHERE owner=?).
- `baseStore`: one per-org SQLite engine at `DBPath = <dataRoot>/orgs/<org>/visor.db`
  (HIP-0302 layout, mirrors hanzo/cloud), CGO-free via `modernc.org/sqlite`
  (xorm driver name `"sqlite"`).

Selected at boot by `storageBackend` (app.conf / `STORAGE_BACKEND`, default
**`base`** — `conf/app.conf` reads `${STORAGE_BACKEND||base}`, and no deployment
sets the variable, so production runs Base) + `dataRoot` (`DATA_ROOT`, default
`/data`). Every model routes its query through one of three package entry
points, so the same xorm query runs unchanged on either backend:
- `object.EngineFor(owner)` — per-tenant rows (per-org SQLite under Base).
- `object.Shared()` — the shared tables (Plan, MeterLease). Cross-POD only under
  Postgres, where the insert-once lease PK holds cluster-wide. Under Base it is
  the pod-local `_global` SQLite coord (durable via object-store hydrate/ship,
  but not shared between concurrent pods), so exactly-once there rests on the
  single-writer election, not the PK. (This line previously claimed "Postgres
  under BOTH backends" — false, and the reason the billing gate looked optional.)
- `object.allEngines()` — the union set a cross-org sweep reads (one engine
  under Postgres; one per org DB under Base).

Call-site migration is DONE: no model touches `adapter.engine` anymore
(`adapter.engine` survives only inside `InitStore`, which hands it to the
providers). `MigratePostgresToBase` (`object/migrate_base.go`, never at boot)
copies `perOrgModels()` rows grouped by Owner into the per-org DBs. The canonical
Base adopter to pattern-match is **hanzo/cloud** (HIP-0302/HIP-0106).

#### Base backend: shared vs per-org (CTO decisions, RESOLVED)
The 10 tables split into two classes, defined once each in `object/store.go`:
- `perOrgModels()` (8): Asset, Provider, Machine, Record, Session, NodePool,
  Volume, AgentBinding — one physical SQLite copy per org under Base.
- `sharedModels()` (2): Plan, MeterLease — live on the ONE shared engine every
  pod sees (Postgres, both backends). `models()` = the two unioned; only the
  Postgres adapter hosts all 10, Base syncs only `perOrgModels()` into org DBs.

1. **MeterLease → shared (Postgres), never per-org.** It is a cluster-global
   leader-election lease whose entire job is money safety: exactly ONE replica
   sweeps hourly billing per wall-clock hour. Two pods with separate local SQLite
   files would each "win" the insert-once PK and double-debit. Leader election
   needs a single linearizable store; per-org SQLite (local, eventually-replicated)
   cannot be one. `ClaimMeterHour`/`pruneMeterLeases` route through `Shared()`.

2. **Plan → shared read-only catalog (Postgres), Provider → per-org.** Plan is a
   global catalog identical for every org; duplicating 11 rows into every org DB
   breaks DRY and lets an admin price edit diverge pod-to-pod, so all Plan CRUD
   routes through `Shared()` (the `owner` column is kept for white-label scoping;
   the physical table is shared). **Provider is NOT a catalog** and stays per-org:
   it holds per-org cloud credentials (`ClientId`/`ClientSecret`, masked `***` on
   read) and per-org blockchain config — routed by `EngineFor(owner)`. (This
   corrects the original "Plan/Provider = catalogs" framing: only Plan is global.)

3. **Cross-org sweeps fan out over `allEngines()`.** `GetAllNodePools` (hourly
   billing report) and `GetSessionsByStatus` (stale-session GC) have no owner —
   they read every tenant. Under Postgres that is one query; under Base there is
   no table spanning tenants, so they union each org DB. `baseStore.AllEngines()`
   enumerates `<dataRoot>/orgs/*` on disk, so it sees every org ever written, not
   just those opened this process lifetime.

4. **Durability & replication (STAGED — the one deliberately-deferred piece).**
   Per-org SQLite is WAL-mode local to the pod's `dataRoot`. In production
   `dataRoot` is a **persistent volume**, so an org DB survives a pod restart.
   What is NOT yet built: serving one org from >1 pod concurrently. SQLite is
   single-writer, and local files diverge between pods, so **Base mode is safe
   today only under `replicas: 1` OR org-sharded routing** (each org pinned to one
   pod). Lifting that needs WAL→object-storage replication with
   single-writer-per-org coordination (hanzo/cloud's `internal/org/replica.go` +
   `internal/storagelock` is the pattern). Exact integration point when it lands:
   hydrate-on-open inside `baseStore.EngineFor` (pull the latest object-storage
   snapshot before first use) plus a post-commit WAL shipper; the lease primitive
   already in `MeterLease`/`Shared()` provides the single-writer election. No code
   stub is shipped for this (no dead abstraction) — this note IS the staging.
   NOTE: this staging note predates the backend default flipping to `base`. Base
   IS the production backend today (nothing sets `STORAGE_BACKEND`), which is why
   `replicas: 1` is load-bearing rather than a preference — see the membership
   section below. Opting INTO Postgres remains a separate operator action.

### No Kubernetes client — visor is the multi-cloud path
Visor links **zero** `k8s.io/*` packages. Cluster access belongs to the controllers
whose whole job is reconciling cluster objects (operator, ingress, arc, kms-operator,
dns/operator); visor is deliberately the OTHER path — VMs across DO/Hetzner/Lightsail/
Azure/GCP/Aliyun/Proxmox. Verify with
`grep -rn --include='*.go' '"k8s.io/' . | grep -v vendor` (must be empty) — the absence
is a property of the build, not of configuration.

Two things used to violate this and are gone:

- **`autoscaler/` (deleted).** A pod-watcher that listed Pending pods via
  `rest.InClusterConfig` and scaled DOKS node pools. It was a cluster autoscaler living
  in the VM manager, and it was gated on `autoscalerClusters`, which is set in no
  deployment (not in `universe/charts/app/values/hanzo/visor.yaml`, not in the visor
  Secret) — so it had never run. `service/doks.go` is untouched: it is pure `godo` and
  still serves node-pool CRUD.
- **`object/coordinator.go` k8s membership → `ha.Membership` seam.** The billing
  single-writer election needs the live replica set; it used to get it by listing
  `app=visor` pods. It now elects over `ha.Membership` (the interface hanzoai/ha already
  defines — reused, not re-declared), installed by `object.RegisterMembership`.
  `BuildMembership()` never returns nil; with nothing registered it returns a source
  that reports an **error** (not an empty set — an empty set would assert "I have no
  peers", the exact lie that double-debits), so `billingOwner()` is false and no lease
  is claimed. It logs the reason once per process.

  **RESOLVED — single writer by topology.** With no source registered visor does not
  meter at all (fail-closed: a missed hour is reconciled, a double debit is not), so a
  source had to be chosen. It is `ha.Static`, registered in `main.go`'s `serve()`, paired
  with `replicas: 1` in `universe charts/app/values/hanzo/visor.yaml`. The sole process
  is the sole writer, so exactly-once holds by construction rather than by election.

  **These two are ONE change and must never drift.** Registering `ha.Static` while
  running `replicas: 2` DOUBLE-BILLS every customer every hour — under the Base backend
  `Shared()` is a pod-local SQLite coord, so the insert-once PK does not span pods and
  nothing else holds the line. Both sides carry a comment pointing at the other. Raising
  replicas REQUIRES registering a real membership source in the same change.

  It is registered in `serve()`, NOT in `visor.Bootstrap()`, because Bootstrap is shared
  verbatim with the embedded cloud mount, whose replica count is cloud's and not ours.
  The embedded path registers nothing and therefore still fails closed — correct, since
  the "I am alone" claim is only true of this Deployment.

  The alternative was `STORAGE_BACKEND=postgres`, which makes `Shared()` one cluster-wide
  engine so the lease PK spans pods and election becomes unnecessary entirely. It keeps
  HA and matches the house rule (Postgres for production multi-instance), but it moves
  ALL org data, not just the coord — `EngineFor` switches from per-org SQLite to the
  single engine. That is a data migration, deferred deliberately.

  `k8s/rbac.yaml` is now a bare ServiceAccount: visor holds no cluster permissions.

### Provider Adapters (all fully implemented)
| Provider | Machine | Volume | File |
|----------|---------|--------|------|
| Hetzner | Yes | Yes | `service/hetzner.go`, `service/volume_hetzner.go` |
| Lightsail | Yes | — | `service/lightsail.go` |
| DigitalOcean | Yes | Yes | `service/digitalocean.go`, `service/volume_digitalocean.go` |

Factory pattern in `service/` dispatches to provider via `MachineClientInterface`.

### Plan Catalog
11 tiers from $5/mo (Starter) to $3,999/mo (Ultra), defined in `object/plan_seed.go`.
Plans are seeded on first boot via `SeedDefaultPlans()`. Pricing is in cents (e.g. 500 = $5).

Provider mapping is internal JSON per plan — customers never see backend provider names.

### Resell Compute (canonical `/v1`, house DigitalOcean account)
The `/v1` resell surface (`service/compute.go`, `controllers/compute.go`) sells
compute over ONE Hanzo house DO account (distinct from the per-owner BYOC
Provider path in `machine_cloud.go`). Endpoints (envelope `{status,msg,data}`):

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/v1/regions` | Cached DO regions catalog |
| GET | `/v1/sizes` | Cached sizes, Hanzo resale price only |
| GET | `/v1/gpus` | GPU sizes (H100/H200/MI300X/L40S/…), resale-priced |
| GET | `/v1/machines` | Caller org's machines (DO tag `hanzo-org:<org>`) |
| POST | `/v1/machines` | `dryRun` → price quote (no spend); real → commerce-gated + provision + first-hour debit |
| GET/DELETE | `/v1/machines/:id` | Get/delete, verified to belong to the org |

The launch VERB left the path: a machine is created in the collection it joins,
so the create is `POST` on the collection and `/v1/machines/launch` — a second
door onto the same one — answers **410** naming its successor (`routers/gone.go`,
`routers/gone_machines.go`). `authz.isResellComputePath` admits the write on that
exact address and nowhere else under the prefix.

The six `/v1/*-machine` addresses are NOT part of this surface and did not move
onto it. They read the visor `machine` TABLE, which `GetMachines`/`GetMachine`
rebuild on every read (`SyncMachinesCloud` deletes every row for the owner and
re-inserts what the org's OWN provider credentials list, keeping only the four
remote-access fields); `/v1/machines` reads live droplets on the house account.
The two disagree on the tenant (`?owner` verbatim against the token's `Owner`
claim), on the item key (`owner/name` against a droplet id, which
`service.GetOrgMachine` parses with `strconv.Atoi`) and on the row shape, and
cloud unions them deliberately (`apps/visor` `managedMachines`). Folding them
would answer from the other store for the other tenant at 200.

A machine's AGENT hangs off the same noun and is the exception to the envelope
row above — those four are TYPED ops with no envelope at all (see "Typed ops"):

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/v1/machines/agents` | The caller org's bindings, `{"agentBindings":[…]}` |
| PUT | `/v1/machines/:id/agent` | Bind a cloud Agent (idempotent) |
| GET | `/v1/machines/:id/agent` | Read one, reconciled; 404 when unbound |
| DELETE | `/v1/machines/:id/agent` | Unbind; 204, the machine stays |

- **Auth (IAM-native, multi-tenant per-org):** callers forward an IAM **Bearer
  JWT** (`object.GetBearerUser`, signature-verified) or a cookie session. The
  token is bound to this **brand by ISSUER** (`iamIssuer`, slash- and
  comma-list-tolerant via `matchIssuer`; empty fails closed) — a sibling brand's
  JWKS token (lux.id/zoo.id/pars.id) is rejected — while **every org within the
  brand is accepted** (`hanzo`, `maxpower`, and every self-service customer), so
  the surface is truly multi-tenant. `resolveComputeOrg` takes the org from that
  user's `Owner` claim (spoof-proof, no `?owner` override; empty Owner fails
  closed). Only an unauthenticated app/service (Basic `clientSecret`) may pass
  `?owner=`. Catalog GETs are public-read; the machines routes are admitted for
  any authenticated brand user by `authz.isResellComputePath` (org-scoping is in
  the controller), so a customer can **list/launch/destroy their OWN** machines,
  not just browse the catalog. Isolation is enforced at the DO layer by the
  `hanzo-org:<org>` tag filter AND in the controller — one tenant can never see
  another's machines. The JWT verify cert is KMS-config (`jwtPublicKeyBase64`);
  the embedded fallback is a non-cert placeholder so a missing cert fails closed
  (never trusts a shipped key).
- **Pricing:** ONE knob in `service/pricing.go` (`HanzoPrice`, base ×1.40, GPU
  ×1.25 over DO list). Wholesale + provider never surfaced (brand policy).
- **Metering:** canonical `github.com/hanzoai/commerce/metering` (Authorize
  gate + Record debit, per-org, real launches only).
- **Secrets (KMS-only):** `houseDOToken()` reads env `DIGITALOCEAN_ACCESS_TOKEN`
  or the KMS-synced `digitalOceanToken` conf key; commerce token from
  `COMMERCE_SERVICE_TOKEN`. Never hardcoded; absent ⇒ fail closed.

### How visor reaches a cloud — one seam, `service/transport.go`
Every provider client is built over an `*http.Client` from `httpFor`, and that
is the ONE place a cloud credential becomes an outbound request. Both SDKs take
one (`godo.NewClient(c)`, `hcloud.WithHTTPClient(c)`), so this is a transport
swap and not an SDK rewrite.

- **Unregistered** (`RegisterCarrier` never called): `directHTTP()` — visor's own
  bounded client, and the SDK attaches the token it was handed. What a local or
  single-binary run wants, and what visor always did.
- **Registered** (`egressAddress` + `egressToken` in conf, wired by `carry()` in
  `egress.go`): the request is described to **hanzoai/egress**, which holds the
  key and attaches it. Reading this pod's env, config or memory yields nothing
  that spends. Visor still holds its OWN token — that is the trade, not an
  oversight: a stolen caller token buys metered calls through our meter rather
  than a vendor key that spends without limit, off our network.
  - `egressAddress` without `egressToken` **refuses to start**. Booting anyway
    would 401 every cloud call, and the obvious repair for that is to unset the
    address and put the keys back.
  - The address is one value: `host:port`, `tcp://host:port`, or
    `unix:///path.sock`.
  - Nothing else to configure: egress knows where each cloud it can pay for
    answers. `EGRESS_URLS` there is an override for a regional endpoint, not a
    requirement.

**A cloud that builds its own transport cannot be carried, and under a carrier
is REFUSED** (`NewMachineClient`, fail-closed). DigitalOcean and Hetzner take
our client; AWS/Azure/GCP/Aliyun/VMware/KVM/PVE build their own. Silently
falling back would put the credential back in the process the carrier exists to
empty — worse than the cloud being unavailable. `TestACloudThatCannotBeCarriedIsRefused`
pins it; egress refuses the same providers for the same reason.

`TestNoUnboundedClient` fails the build on any `http.Client{}` or
`http.DefaultClient` in `service/`: an outbound call with no deadline holds an
org's provisioning lease until the socket closes.

### Key Dependencies
- Go 1.26, `zap-proto/zip` v1.27.0 (the ONE framework), `hanzoai/orm` (the ONE
  ORM); `github.com/hanzoai/commerce/metering` v0.1.4
- `hcloud-go/v2` v2.37, `godo` v1.197, `aws-sdk-go-v2/lightsail`
- `github.com/hanzoai/egress/spend` v0.1.0 — the cloud-call contract and its
  client. A LEAF module on purpose: requiring the egress parent moved authz
  1.10.14 → 1.10.30, which relocated packages visor imports and broke the build.
- **IAM client: `github.com/hanzoai/iamsdk/v2` — the ONE Go client for Hanzo
  IAM.** `InitConfig` is called once at startup (`controllers.InitAuthConfig`)
  and everything else rides the resulting global client: `ParseJwtToken`
  (verify + decode a forwarded Bearer), `GetOAuthToken` (the `/v1/signin`
  callback), `AddUser` (bot registration). `iamsdk.User` / `iamsdk.Claims` are
  the identity types the whole service passes around.

  This used to be `github.com/hanzoai/iam-v1`, whose ROOT package was a second
  copy of the same SDK — 67 of 73 files byte-identical. That repo is ARCHIVED:
  it cannot take a push, so it cannot take a security fix, and a deployed
  service must not depend on one. `deps_test.go` fails the build if it returns.
  `hanzoai/iam` (v2, the server) is still linked transitively through cloud for
  its `pkg/model` types — that is the same lineage, not a second one.

## Key Files
| Path | Purpose |
|------|---------|
| `object/plan.go` | Plan model & CRUD |
| `object/plan_seed.go` | 11-tier plan catalog with provider mappings |
| `object/volume.go` | Volume model |
| `object/volume_cloud.go` | Cloud volume provisioning |
| `object/machine.go` | Machine model & CRUD |
| `object/machine_cloud.go` | Cloud machine provisioning |
| `service/hetzner.go` | Hetzner adapter |
| `service/lightsail.go` | Lightsail adapter |
| `service/digitalocean.go` | DO adapter |
| `controllers/plan.go` | Plan API endpoints |
| `controllers/volume.go` | Volume API endpoints |
| `billing/pricing.go` | Pricing engine |

## Related Repos
- **hanzoai/plans** (private): Canonical plan catalog JSON (plans, regions, storage, GPU pricing)
- **hanzobot/site**: Marketing site consuming pricing.hanzo.ai API
- **hanzo/pricing**: Pricing service at pricing.hanzo.ai

## Brand Policy
Never expose upstream provider names (Hetzner, Lightsail, DO) in public-facing APIs or UI.
Provider mapping is internal only (`ProviderMapping` JSON field on Plan objects).

## CI / Build & Registry (state as of consolidation)

vm is the consolidated Casvisor fork (visor archived; content adopted under the
`vm` repo name + module `github.com/hanzoai/vm`). Default branch is `main`
(master/master_old deleted; nothing lost — the flat-1.30× pricing commit was
cherry-picked in). Image published as `ghcr.io/hanzoai/visor`.

### How this ships

One way, and it runs on our own stack:

    push  ->  github.com/hanzoai/visor        (a mirror)
              .github/workflows/sync.yml       carries refs onward
      ->  git.hanzo.ai/hanzoai/visor           CANONICAL
              .hanzo/workflows/build.yml       builds ghcr.io/hanzoai/visor
      ->  hanzoai/universe crs/visor.yaml      names the tag that is live
      ->  hanzoai/operator                     reconciles the App
      ->  hanzoai/ingress                      serves visor.hanzo.ai

**git.hanzo.ai is canonical; GitHub is a mirror.** `.github/workflows/` holds
exactly one file, `sync.yml`, and its only job is getting refs to the forge. Every
build, check and deploy is a workflow under `.hanzo/workflows/`, which the forge
reads. `.hanzo/workflows` uses GitHub Actions syntax, so a workflow moves between
the two by changing directory and nothing else.

`build.yml` was briefly deleted with no replacement anywhere, which left the
promoted, live `visor` App with **nothing that could build its image** — and no
failing run to show it, because a workflow that does not exist cannot go red. It is
restored at the path the forge reads. That failure mode is the whole reason a
migration is `git mv` and never a delete.

A build never deploys itself: it publishes an image, and `crs/visor.yaml` in
`hanzoai/universe` names which tag is live.

### Build pipeline
`.hanzo/workflows/build.yml` calls the shared `hanzoai/.github` docker-build
workflow. Native per-arch build (no QEMU): amd64 on the `hanzo-build-linux-amd64`
runner, arm64 on spark's arcd (`self-hosted,linux,arm64`); a multi-arch manifest
is composed from the per-arch tags. `build.sh` cross-compiles via
`GOOS=linux GOARCH=${TARGETARCH}` (CGO_ENABLED=0), so each arch builds natively.
Requires `id-token: write` (cosign/SBOM), else the reusable workflow fails at
startup.

### Base images — ghcr.io/hanzoai/* (NOT Docker Hub / ECR)
The Dockerfile pulls golang/node/alpine/guacd from `ghcr.io/hanzoai/*` (mirrored
there; alpine variants, small/reliable). Docker Hub is rate-limited (429) for
the shared-egress runners; ECR is not used (not ours).

**RESOLVED — and the diagnosis above was wrong.** The 403 story was never
verified against a real run. On the platform build lane the base images pull
FINE (`golang`, `alpine`, `guacd` are still private to an anonymous puller, and
it does not matter: the builder authenticates with the org-level `GH_PAT`, not
`GITHUB_TOKEN`). A real BuildKit run reached `[back 6/6]` — i.e. past every
`FROM` — before failing.

The actual reason no image existed for any tag after v1.108.12:

    go: go.mod requires go >= 1.26.5 (running go 1.26.4; GOTOOLCHAIN=local)

`cd0530f` bumped the go directive to 1.26.5; `ghcr.io/hanzoai/golang:1.26-alpine`
ships 1.26.4 and sets `GOTOOLCHAIN=local`, so every build died at the first go
command. The routes added in v1.108.13 therefore never reached an image, and
`/v1/k8s/nodes` answered `visor: upstream 404` for weeks while the code that
served it was sitting on main. Fixed with `GOTOOLCHAIN=auto`: the module pins the
toolchain, the base image is free to lag. First landed in `build.sh` (v1.108.17),
now set once as `ENV GOTOOLCHAIN=auto` in the Dockerfile's BACK stage — the same
place every other Hanzo Go builder sets it, and it covers every `go` command in
the stage rather than only the ones inside `build.sh`.

**The base image stays on `ghcr.io/hanzoai/golang:1.26-alpine`.** An anonymous
puller gets HTTP 403 with zero listable tags, which reads like a dead mirror and
has now been mistaken for one twice. It is not: `hanzoai/golang`, `alpine` and
`guacd` are *private* packages, so GHCR refuses to mint an anonymous scope token
(`hanzoai/node` and `hanzoai/visor` are public and do mint one — that asymmetry
is the tell). The builder authenticates with the org-level `GH_PAT`, and a real
BuildKit run reached `[back 6/6]`, past every `FROM`. The tag is deliberately
left floating rather than pinned to a patch: the mirror's tag list cannot be
enumerated without `read:packages`, so pinning to a tag nobody can confirm exists
would trade a working build for a `manifest unknown` failure. `GOTOOLCHAIN=auto`
is what makes a floating base tag safe.

Lesson worth keeping: a RED build had a plausible, documented, and *false*
cause recorded next to it. Nobody had run the build since writing it down. Read
the failing log, not the note about the failing log.

### Cutting a release — which lane actually works

Two lanes exist and they fail differently. Know which one you are on.

**Tags must reach the FORGE.** `git.hanzo.ai/hanzoai/visor` is canonical; GitHub
is a mirror. The build lane lives at `.hanzo/workflows/build.yml`, which only
Gitea Actions reads — `.github/` has carried zero workflows since `942e2f4`.
v1.108.15 and v1.108.16 were pushed to GitHub ONLY, so the forge never saw the
tag, nothing triggered, and the tag looked cut while no image existed. Push tags
to BOTH remotes, and confirm with
`git ls-remote --tags https://git.hanzo.ai/hanzoai/visor.git 'refs/tags/v*'`.

**Gitea Actions can be wedged instance-wide, and it looks like "queued".**
Hanzo Git runs on SQLite (`/data/git/git.db`, `SQLITE_TIMEOUT = 5000`). When the
DB bloats, `CreateTaskForRunner` exceeds 5000ms, every `FetchTask` returns 500,
and NO runner is ever assigned ANY task in ANY repo — while the runners sit
online and idle and the UI just says `queued`. Seen at 8.25 GB with 1,853,355 of
2,013,625 pages on the free list (92% bloat) and a 4.1 GB WAL that would not
checkpoint (`wal_checkpoint(TRUNCATE)` -> `BUSY`, 50 of 1,000,794 frames). Check
`kubectl logs -n hanzo deploy/hanzo-git | grep "pick task failed"` before
believing a queue is just slow. Real fix needs Gitea quiesced (VACUUM), so it is
a planned-downtime decision, not something to do mid-release.

**The image-build lane that does NOT depend on any of that** is platform, and it
is the documented one way to build (universe/LLM.md):

    curl -X POST https://platform.hanzo.ai/v1/runner \
      -H "Authorization: Bearer $PLATFORM_BUILD_CALLBACK_TOKEN" \
      -H 'Content-Type: application/json' \
      -d '{"repo":"hanzoai/visor","sha":"<sha>",
           "image":"ghcr.io/hanzoai/visor:<tag>",
           "organizationId":"Yb5GFGDBEwcLsv2O8qWjS",
           "ref":"refs/tags/<tag>","dockerTarget":"STANDARD"}'

`organizationId` is required and is a FOREIGN KEY into platform's `organization`
table — the slug `hanzo` and an IAM UUID both fail with `FOREIGN KEY constraint
failed`. The value is `Yb5GFGDBEwcLsv2O8qWjS`. Watch it with
`kubectl logs -n hanzo-build job/build-visor-<id>`. This is how v1.108.17 was
built; it runs BuildKit in-cluster, so it never builds on anyone's laptop.

### Images live on oci.hanzo.ai, built on platform.hanzo.ai
Builds run on our own platform and images land in our own registry, not GitHub.
- `oci.hanzo.ai` is the image and Helm-chart host, org-namespaced
  `oci.hanzo.ai/<org>/<app>`. Auth is Hanzo IAM: `/v2/` answers
  `401 Bearer realm="https://hanzo.id/v1/iam/registry/token", service="oci.hanzo.ai"`.
  Push needs an IAM admin user OR the `hanzo-registry` IAM **application**
  client_id:client_secret (the machine/CI path, distributed via KMS — see
  `iam/controllers/registry_token.go`). Pull authenticates too.
- `platform.hanzo.ai` (dokploy fork) has docker-file/nixpacks/paketo builders
  and builds visor from its Dockerfile → push `oci.hanzo.ai` → deploy DOKS.
- `hanzoai/ci` mirrors each published tag with a server-side crane copy:
  `ghcr.io/<org>/<name>` → `oci.hanzo.ai/<org>/<name>`, where the GHCR org
  `hanzoai` maps to the registry org `hanzo`. That copy is best-effort, so a
  registry outage leaves the tag on GHCR only.
