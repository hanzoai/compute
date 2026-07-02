# Visor

## Overview
Visor is Hanzo's multi-provider cloud VM management platform. It provisions, monitors, and manages virtual machines across Hetzner, AWS Lightsail, and DigitalOcean (plus Azure, GCP, Aliyun, Proxmox, VMware, KVM). Originally forked from Casvisor, fully rebranded to `github.com/hanzoai/visor`.

## Architecture

### Core Layers
- **Controllers** (`/controllers/`): HTTP handlers (Beego framework), JWT auth
- **Service** (`/service/`): Provider adapters implementing `MachineClientInterface`
- **Object** (`/object/`): Data models, DB operations (XORM), plan seeds
- **Billing** (`/billing/`): Pricing engine
- **AuthZ** (`/authz/`): hanzoai/authz-based authorization

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
| POST | `/v1/machines/launch` | `dryRun` → price quote (no spend); real → commerce-gated + provision + first-hour debit |
| GET/DELETE | `/v1/machines/:id` | Get/delete, verified to belong to the org |

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

### Key Dependencies
- Go 1.26, Beego 1.12, XORM; `github.com/hanzoai/iam` v1.28.12;
  `github.com/hanzoai/commerce/metering`; `godo` v1.184
- `hcloud-go/v2` v2.36, `godo` v1.175, `aws-sdk-go-v2/lightsail` v1.20
- `hanzoid/go-sdk` v1.45 (IAM integration)

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

### Build pipeline
`.github/workflows/build.yml` calls the shared `hanzoai/.github` docker-build
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

**OPEN — build is RED until these 4 packages are made public:**
`golang`, `node`, `alpine`, `guacd` under github.com/orgs/hanzoai/packages →
each → Package settings → Change visibility → Public. The shared workflow logs
into ghcr with `GITHUB_TOKEN`, which cannot pull *cross-repo private* packages
(403 on `FROM ghcr.io/hanzoai/golang`). Making them public fixes it; then
re-run the build and both arches go green. (Flipping visibility needs a
packages-scoped token / the org web UI — not doable from the build host.)

### Preferred future: registry.hanzo.ai + platform.hanzo.ai (off GitHub)
Directive: build on our own platform, images in our own registry, not GitHub.
- `registry.hanzo.ai` = Docker registry with IAM-backed token auth
  (`realm=https://iam.hanzo.ai/v1/iam/registry/token`). Push needs an IAM admin
  user OR the `hanzo-registry` IAM **application** client_id:client_secret
  (the machine/CI path, distributed via KMS — see
  `iam/controllers/registry_token.go`). No anonymous pull today.
- `platform.hanzo.ai` (dokploy fork) has docker-file/nixpacks/paketo builders
  and can build vm from its Dockerfile → push registry.hanzo.ai → deploy DOKS,
  removing GitHub Actions from the loop.
- To wire this: provide the `hanzo-registry` app credential (or a KMS service
  token), then register vm as a platform app. Blocked on that credential.
