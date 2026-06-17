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

### Key Dependencies
- Go 1.24, Beego 1.12, XORM
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
