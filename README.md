# Hanzo Visor

**The multi-cloud compute plane for Hanzo Cloud — machines, GPUs, and clusters across AWS, GCP, Azure, DigitalOcean, and bare metal.**

![Go 1.24](https://img.shields.io/badge/Go-1.24-00ADD8) ![Compute](https://img.shields.io/badge/compute-AWS%20%C2%B7%20GCP%20%C2%B7%20Azure%20%C2%B7%20DO-informational) ![License](https://img.shields.io/badge/license-Apache--2.0-blue)

Hanzo Visor manages physical, virtual, and containerized compute across multiple cloud providers. It allows tenants to launch, resize, and terminate machines, configure GPU accelerators, attach block storage, and connect BYO Kubernetes clusters under one unified API (`api.hanzo.ai/v1/visor/*`) and console interface.

## Features

- **Multi-Cloud Compute Passthrough** — Native drivers for AWS EC2, Google Compute Engine, Azure Virtual Machines, DigitalOcean Droplets, and Hetzner.
- **BYO Kubernetes Clusters** — Attach external clusters via KMS-sealed kubeconfig credentials into the unified fleet.
- **Unified Identity & Access** — Tenant isolation and user authentication powered by [Hanzo IAM](https://github.com/hanzoai/iam) (`hanzo.id`).
- **Integrated Metering & Spend Caps** — Real-time compute usage attribution (`hanzo.compute_usage`) and billing gates integrated with Hanzo Commerce.
- **Block Storage Orchestration** — Dynamic provisioning, resizing, snapshotting, and attaching of cloud block volumes.

## Architecture

Visor acts as the compute engine behind the unified Hanzo Cloud binary:

```
                  ┌──────────────────────┐
                  │    console.hanzo.ai  │
                  └──────────┬───────────┘
                             │
                             ▼
                  ┌──────────────────────┐
                  │     Hanzo Cloud      │
                  │  (/v1/visor/clusters)│
                  └──────────┬───────────┘
                             │
                             ▼
                  ┌──────────────────────┐
                  │     Hanzo Visor      │
                  │    (Compute Plane)   │
                  └──────────┬───────────┘
         ┌────────────┬──────┴───────┬────────────┐
         ▼            ▼              ▼            ▼
     ┌───────┐   ┌─────────┐   ┌───────────┐  ┌───────┐
     │  AWS  │   │   GCP   │   │   Azure   │  │  DO   │
     │ (EC2) │   │  (GCE)  │   │   (VMs)   │  │(DOKS) │
     └───────┘   └─────────┘   └───────────┘  └───────┘
```

## API Surface

| Endpoint | Method | Description |
|---|---|---|
| `/v1/visor/clusters` | `GET` | List managed and attached BYO clusters |
| `/v1/visor/clusters` | `POST` | Attach a BYO cluster with sealed kubeconfig |
| `/v1/visor/clusters/:id` | `DELETE` | Detach a BYO cluster from the fleet |
| `/v1/visor/machines` | `GET` | List active virtual machines and instances |
| `/v1/visor/machines` | `POST` | Launch an instance in a selected cloud and region |
| `/v1/visor/machines/:id` | `DELETE` | Terminate an instance |
| `/v1/visor/volumes` | `GET` | List block storage volumes |
| `/v1/visor/volumes` | `POST` | Create or attach a storage volume |

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for third-party attributions.
