# Hanzo Compute

**Machines and GPUs for Hanzo Cloud, launched in Hanzo's own AWS account and billed by the hour.**

![Go 1.24](https://img.shields.io/badge/Go-1.24-00ADD8) ![Compute](https://img.shields.io/badge/compute-AWS%20%C2%B7%20Hetzner-informational) ![License](https://img.shields.io/badge/license-Apache--2.0-blue)

Compute launches, starts, stops and terminates machines for an org, prices every size from one catalog, and debits the org's prepaid balance through Hanzo Commerce. It holds no cloud credential: every call to a cloud goes through [hanzoai/egress](https://github.com/hanzoai/egress), which keeps the account's credential in KMS, signs the request in memory and returns only the answer.

## Features

- **Hosted machines** — EC2 in Hanzo's account, x86_64 CPU and GPU sizes, one encrypted gp3 root volume each, IMDSv2 only.
- **No key in compute** — AWS is reached through egress, which assumes the `hanzo-compute` role with its own IAM identity. A provider row stores a label and a region, never a key, and a write carrying one is refused.
- **Per-org identity** — Hanzo IAM (`hanzo.id`) bearer tokens, scoped by the org a caller is a signed member of. The one platform privilege is acting in the `admin` org.
- **Metering** — the first hour is authorized and debited before a launch; every later running hour, and a stopped machine's disk, is debited hourly. A machine its org cannot pay for is stopped.

## Architecture

```
  console.hanzo.ai / api.hanzo.ai
                │
                ▼
        Hanzo Compute ──── balance, debits ───▶ Hanzo Commerce
                │
                │ ZAP, its own IAM token
                ▼
          hanzoai/egress ── KMS: cloud/aws/hanzo-compute/credential
                │
                │ SigV4, signed in memory
                ▼
       EC2 · CloudWatch (us-east-1)
```

The role egress assumes, and what it may do, is in egress's [`deploy/aws`](https://github.com/hanzoai/egress/tree/main/deploy/aws).

## API Surface

| Endpoint | Method | Description |
|---|---|---|
| `/v1/regions`, `/v1/sizes`, `/v1/gpus` | `GET` | The catalog, with Hanzo's price per running and stopped hour |
| `/v1/machines` | `GET` | The caller org's machines |
| `/v1/machines` | `POST` | Quote (`dryRun`) or launch a machine |
| `/v1/machines/:owner/:name` | `GET`, `PUT`, `DELETE` | Read, start or stop, or terminate one machine |
| `/v1/machines/:owner/:name/agent` | `GET`, `PUT`, `DELETE` | A machine's bound agent |

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for third-party attributions.
