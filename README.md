# fleetdash

A tiny fleet-status dashboard, and — more to the point — a working
**continuous-deployment pipeline** built on **GitHub (repo) + Azure DevOps
(pipeline)**, deploying to hosts across three clouds over Tailscale.

The app is deliberately small. The pipeline is the point.

<!-- badge: add after the ADO pipeline exists
[![Build Status](https://dev.azure.com/<org>/<project>/_apis/build/status/fleetdash?branchName=main)](https://dev.azure.com/<org>/<project>/_build/latest?definitionId=<id>&branchName=main)
-->

## What it does

One Go binary runs on every node. Each instance serves:

| Route | Purpose |
|---|---|
| `GET /` | HTML table: this node + every configured peer (env, cloud, version, host uptime, load, up/down). A down peer shows as a red row; it never breaks the page. Flags **version drift** when nodes disagree on version. Auto-refreshes every 10s. |
| `GET /api/status` | JSON status for *this* node. Peers are just this endpoint fetched over the tailnet. |
| `GET /healthz` | Liveness; the deploy script gates on it. |

All plain HTTP over Tailscale — no TLS, no public exposure. Reach it from any
Tailscale-connected browser.

### The demo

Every node's row shows the **version** (git SHA) it's running. Merge a change and
watch it roll `build → dev → stage → prod` on the dashboard, with prod holding for
a manual approval. The pipeline's effect is the thing on screen.

## Configuration

Environment variables (systemd reads them from `/etc/fleetdash/config.env`; see
[`config.example.env`](config.example.env)):

| Var | Default | Notes |
|---|---|---|
| `FLEETDASH_LISTEN` | `:8080` | listen address |
| `FLEETDASH_NODE` | hostname | display name |
| `FLEETDASH_ENV` | `unknown` | `dev` / `stage` / `prod` / `node` |
| `FLEETDASH_CLOUD` | `unknown` | `aws` / `gcp` / `azure` / `home` |
| `FLEETDASH_REGION` | — | freeform |
| `FLEETDASH_PEERS` | — | comma-separated peer base URLs |

## Local development

Needs Go 1.23+.

```sh
make test          # go vet + go test
make run           # serves on :8080
FLEETDASH_ENV=dev FLEETDASH_PEERS=http://localhost:8081 make run
```

## The pipeline

[`azure-pipelines.yml`](azure-pipelines.yml) — runs in Azure DevOps, triggered by
GitHub via the Azure Pipelines GitHub App.

```
build_test ──▶ deploy_dev ──▶ deploy_stage ──▶ deploy_prod
(vet, test,     (auto,          (auto,            (manual approval
 build both     on main)         after dev)        on the ADO
 arches)                                           Environment)
```

- **Self-hosted agent** on `ubuntu2404adm` (pool `lab-selfhosted`). It's already on
  the tailnet, so it can reach every deploy target; a Microsoft-hosted agent would
  have to join Tailscale each run. Safe on a public repo because ADO does not build
  fork PRs by default.
- **Hermetic build**: the pinned Go toolchain is downloaded per run, so moving to
  Microsoft-hosted agents later is a one-line `pool:` change.
- **Two architectures**: `dev`/`stage` are x86-64, `prod` is arm64 (Graviton). Both
  binaries are built every run; see *Architecture drift* below.
- **Deploy** = `deploy/deploy.sh` copies the binary to the target and runs
  `sudo /opt/fleetdash/bin/apply-release`, which installs it, keeps the previous
  binary, restarts the service, health-checks, and **rolls back on failure**.

### Architecture drift (known, accepted)

Tests run on the agent (amd64) only; the arm64 build is compile-checked but not
test-run, because there's no arm64 host in the lower environments. The x86 micros
are x86 because that's what the GCP/Azure free tiers provide. A heterogeneous
x86/arm64 fleet is realistic; the pipeline handles it by building and shipping the
right binary per environment.

## Provisioning a target

Once per host, from a checkout, as root:

```sh
sudo FLEET_ENV=dev FLEET_CLOUD=azure FLEET_REGION=eastus \
     FLEET_PEERS='http://stage-host:8080,http://prod-host:8080,http://adm-host:8080' \
     DEPLOY_PUBKEY="$(cat fleetdash_deploy_key.pub)" \
     ./deploy/provision-target.sh
```

Creates an unprivileged `fleetdash` service account, the systemd unit, the
root-owned `apply-release` helper, and a `deploy` login user whose **only** sudo
right is `apply-release`.

## Not in this repo

- Real tailnet hostnames — pipeline variables `DEV_HOST` / `STAGE_HOST` /
  `PROD_HOST`, set in the ADO UI.
- The deploy SSH key — an ADO **secure file** named `fleetdash_deploy_key`.

## Roadmap (not built)

- adm as a 4th monitored node
- historical uptime trending via a Prometheus `/metrics` endpoint + Prometheus/Grafana
- a node-to-node latency grid
- anomaly panel (drift, clock skew, disk)
- `tailscale serve` for real certs

## License

MIT — see [LICENSE](LICENSE).
