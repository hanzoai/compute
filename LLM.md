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

- **Auth (IAM-native, per-org):** `resolveComputeOrg` uses the signed-in user's
  IAM `Owner` claim (spoof-proof); a trusted app/service call (visor client
  secret, like the console proxy) may pass `?owner=`. Catalog GETs are
  public-read (authz policy). One tenant can never see another's machines —
  isolation is enforced at the DO layer by the `hanzo-org:<org>` tag filter.
- **Pricing:** ONE knob in `service/pricing.go` (`HanzoPrice`, base ×1.40, GPU
  ×1.25 over DO list). Wholesale cost and the upstream provider are never
  surfaced (brand policy; margin stays private). Supersedes the stale hardcoded
  `billing/pricing.go` wholesale-cost map (kept for node-pool cost reporting).
- **Metering:** canonical `github.com/hanzoai/commerce/metering` — `Authorize`
  gate (fail-closed) + `Record` debit, per-org, on real launches only.
- **Catalog cache:** in-memory (`sync.RWMutex`), 24h TTL for regions/sizes;
  live droplets never cached.

### Secrets (KMS-only)
DO house token and commerce token come from KMS, never hardcoded:
- `digitalOceanToken` lives in KMS `hanzo/prod/visor-config` (app.conf blob),
  synced by KMSSecret `visor-kms-sync` → secret `visor-config` → `/conf/app.conf`.
  `service.houseDOToken()` reads env `DIGITALOCEAN_ACCESS_TOKEN` first, then the
  `digitalOceanToken` conf key. Seed with the KMS admin identity `hanzo-kms`:
  `POST https://kms.hanzo.ai/v1/kms/orgs/hanzo/secrets` (Bearer = IAM
  client_credentials JWT from `https://hanzo.id/v1/iam/oauth/access_token`).
- `COMMERCE_SERVICE_TOKEN` (env, KMS-wired) authorizes metering; absent ⇒ real
  launches fail closed while quotes still work.

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
