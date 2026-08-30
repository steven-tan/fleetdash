# fleetdash — build walkthrough

A paced explanation of how this project is put together and *why* each piece is
the way it is. The [README](../README.md) is the quick reference; this is the
"sit down and understand it" version.

---

## 1. What this is

A small Go web app (`fleetdash`) that runs on several hosts across different
clouds. Each instance reports its own status and renders a table of the whole
fleet. That app is the *excuse*; the real subject is the **CI/CD pipeline** that
builds and deploys it:

```
git push  ─▶  build + test  ─▶  deploy dev  ─▶  deploy stage  ─▶  deploy prod
   (GitHub)      (Azure DevOps, on a self-hosted agent)            (manual approval)
```

Every step after `git push` is automated. The only human action in the happy path
is approving the production deployment.

### Two planes that only share a login

The single biggest thing to keep straight:

| Plane | What it holds | Analogy |
|---|---|---|
| **GitHub** | the git repository, nothing else | CodeCommit |
| **Azure DevOps** | the pipeline, agent pools, environments, secrets | CodeBuild + CodePipeline + CodeDeploy |
| **The clouds** (AWS / GCP / Azure) | the VMs we deploy *to* | EC2 / Compute Engine / Azure VMs |

Azure DevOps and "Azure the cloud" are **different products**. An ADO *project*
cannot contain a VM. ADO only ever reaches into a cloud through credentials you
give it. Here it reaches the target VMs over SSH, using a key stored in ADO.

---

## 2. The repository

| Path | Role |
|---|---|
| `main.go` | the whole app — one file, standard library only |
| `main_test.go` | unit tests the pipeline runs |
| `go.mod` | module definition, Go version floor, zero dependencies |
| `azure-pipelines.yml` | the pipeline definition (read by Azure DevOps) |
| `deploy/deploy.sh` | runs on the build agent; ships a binary to one target |
| `deploy/apply-release` | runs on each target as root; installs + restarts + health-checks + rolls back |
| `deploy/provision-target.sh` | one-time host setup (users, dirs, unit, sudoers) |
| `systemd/fleetdash.service` | the service unit installed on every target |
| `config.example.env` | template for the per-host config file |
| `Makefile` | local convenience (`make run`, `make test`, `make dist`) |

Nothing about a specific environment is committed — no real hostnames, no keys.
Those live in Azure DevOps (see §6, §9).

---

## 3. The app

### Endpoints

- `GET /api/status` — JSON describing *this* node: env, cloud, region, version,
  arch, process/host uptime, load. Peers are just this endpoint, fetched over the
  network.
- `GET /` — an HTML table: this node plus every configured peer, fetched
  concurrently. A peer that's down becomes a red row with the error; it never
  breaks the page. If nodes disagree on `version`, a drift banner appears.
- `GET /healthz` — returns `ok`. The deploy script gates on this.

### Configuration comes from the environment

The app reads everything from environment variables (`FLEETDASH_ENV`,
`FLEETDASH_CLOUD`, `FLEETDASH_PEERS`, …). It has no config file of its own and no
flags.

Why: **the same binary runs on every host**, but each host needs different values
(its env label, its cloud, its list of peers). Externalising that into the
environment — rather than baking it into the binary or having per-host builds — is
the [12-factor](https://12factor.net/config) approach. Changing the peer list is
then an edit to one file plus a service restart, with no rebuild.

### Version is stamped at build time

`var version = "dev"` in the source is overwritten by the linker during the build:

```
go build -ldflags "-X main.version=<git sha>" ...
```

So every commit produces a binary that knows which commit it came from, and that
string shows on every row of the dashboard. Watching it change across dev → stage
→ prod *is* the deployment, made visible.

### Why Go

A pure-Go program compiles to a single static binary with no runtime and no
shared-library dependencies (`CGO_ENABLED=0` guarantees this). Deploy is "copy one
file, restart the service." Rollback is "copy the previous file back." No
interpreter, no virtualenv, no `npm ci` on the target. It also cross-compiles
trivially, which matters here (see §11).

---

## 4. The pipeline

`azure-pipelines.yml` defines four stages.

### `build_test` — on the agent

1. **Install Go.** The pinned version (`GO_VERSION`) is downloaded and unpacked
   per run. This is deliberate: the build then depends only on what the pipeline
   declares, not on whatever happens to be installed on the agent. It also keeps
   the agent host disposable and makes a future switch to Microsoft-hosted agents
   a one-line change.
2. **`go vet` + `go test`.** Static checks and unit tests. Runs on the agent's
   architecture only (see §11).
3. **Build both binaries.** `linux/amd64` and `linux/arm64`, with the version
   stamped in. Output goes to the staging directory.
4. **Publish artifact.** The two binaries are uploaded as a pipeline artifact
   named `binaries`, so the deploy stages can download exactly what was built —
   not rebuild.

### `deploy_dev` / `deploy_stage` / `deploy_prod`

Each is a **deployment job** targeting an **environment** (`fleetdash-dev`, etc.).
Deployment jobs differ from regular jobs: they record deployment history against
the environment, and they don't check out the repo by default (so each has an
explicit `checkout: self` to get the `deploy/` scripts).

Each deploy stage:

1. downloads the `binaries` artifact,
2. downloads the SSH key from **secure files**,
3. runs `deploy/deploy.sh` against the right host with the right binary.

Flow control:

- `deploy_dev` runs only on `main` (`condition: eq(Build.SourceBranch, 'refs/heads/main')`).
  Pull-request builds get `build_test` only.
- `deploy_stage` runs after `deploy_dev` succeeds.
- `deploy_prod` runs after `deploy_stage` succeeds **and** a human approves (§9).
- dev and stage get the `amd64` binary; prod gets `arm64` (§11).

---

## 5. The build agent

The pipeline runs on a **self-hosted agent** — a small service process on our
admin box (`adm`) that polls Azure DevOps for work.

### Why self-hosted rather than Microsoft-hosted

1. A brand-new Azure DevOps organisation can't use Microsoft-hosted agents until
   you request a free-parallelism grant, which takes a few business days. The
   self-hosted path has no such wait.
2. The deploy targets are only reachable over our private mesh network. The
   admin box is already on that mesh; a Microsoft-hosted agent would have to join
   it on every run.

### How it's set up

- A dedicated unprivileged user (`azp`) runs the agent — **never** a user with
  broad rights. A pipeline checks out repo code and *executes it* on the agent,
  so the agent's identity is a blast-radius boundary.
- The agent is installed as a systemd service so it survives reboots.
- Registration used a short-lived Personal Access Token scoped to
  "Agent Pools (read & manage)". That token is used **once**, during
  registration; the agent then negotiates its own long-lived credentials. The PAT
  can be revoked immediately afterward.

### Making the build hermetic pays off here

Because the pipeline installs its own Go toolchain, we never had to install Go on
the admin box. The agent host stays a generic machine. If we later move to
Microsoft-hosted agents, the only change is the `pool:` line.

---

## 6. Secrets

The only real secret in the whole system is the **SSH private key** the pipeline
uses to reach the targets. (The app itself has no secrets — it's a read-only
dashboard.)

It's stored as an Azure DevOps **secure file**: encrypted at rest, downloaded
into the job at runtime by the `DownloadSecureFile` task, and never written to
git. Secure files can't be downloaded back out through the UI — only a pipeline
run can retrieve one — so a copy also lives in a password manager for
recovery/rotation.

### The tiers, for reference

| Tier | Example | Notes |
|---|---|---|
| plaintext in git | value in YAML | never, for secrets |
| pipeline secret variable / secure file | this project | encrypted at rest, masked in logs; static, lightly audited |
| secret manager | Vault, Azure Key Vault, AWS Secrets Manager | adds versioning, audit, rotation, dynamic/short-lived creds |

A secret manager is the "grown-up" answer. For a single static SSH key in a lab,
a secure file is proportionate. An `ado variable group linked to Azure Key Vault`
is the low-effort upgrade if a real app secret ever appears.

---

## 7. The three identities on each target

`provision-target.sh` sets these up once per host:

| User | Purpose | Privilege |
|---|---|---|
| **`fleetdash`** | runs the long-lived app process (`User=fleetdash` in the unit) | none — can't log in, owns only `/opt/fleetdash` |
| **`deploy`** | the account the pipeline SSHes in as | may `sudo` **exactly one** command: `/opt/fleetdash/bin/apply-release` |
| **root** (via that one `sudo` rule) | `apply-release` installs the binary and restarts the service | full, but only reachable through that one script |

This is least privilege made concrete. If the app is ever exploited, the attacker
is `fleetdash` — a no-login account that owns nothing interesting. The pipeline's
`deploy` account can't run arbitrary commands as root; it can run one audited
script. On the production host (which also serves real traffic) this separation is
the point.

The `sudo` rule is a single line in `/etc/sudoers.d/`, validated with `visudo -cf`
during provisioning:

```
deploy ALL=(root) NOPASSWD: /opt/fleetdash/bin/apply-release
```

---

## 8. How a deploy actually runs

`deploy/deploy.sh` (on the agent) does two things over SSH:

1. `scp` the new binary to `/tmp/fleetdash.new` on the target.
2. `ssh <target> 'sudo /opt/fleetdash/bin/apply-release'`.

`deploy/apply-release` (on the target, as root) does the rest:

1. install the new binary next to the current one,
2. copy the current one to `fleetdash.prev` (rollback point),
3. move the new one into place, `systemctl restart fleetdash`,
4. wait, then `curl` the local `/healthz`,
5. **if the health check fails**, restore `fleetdash.prev`, restart again, and
   exit non-zero — which fails the pipeline stage.

So a bad build that starts but doesn't pass its health check is automatically
rolled back on that host, and the pipeline goes red instead of silently shipping
it onward.

---

## 9. The two gates in front of production

On the first run, production is blocked twice, for two different reasons:

1. **Resource authorization** ("permission needed"). The first time a pipeline
   uses any protected resource — an environment, a secure file — Azure DevOps
   pauses and makes the pipeline owner explicitly permit it. One-time, per
   resource. This is *not* an approval of the deployment.
2. **The approval check.** Configured on the `fleetdash-prod` environment:
   a named approver (you) must click Approve before `deploy_prod` runs. This
   fires on every run, and it's the "continuous delivery, not continuous
   deployment" line — production waits for a human.

dev and stage have no approval check, so they flow automatically.

---

## 10. Per-host config, and how it scales

Each target has `/etc/fleetdash/config.env`, written by `provision-target.sh` and
read by systemd (`EnvironmentFile=`) which injects it into the service process.

This file is **generated, not hand-maintained** — its values are mostly derived
(region from the cloud's metadata service, node name from `hostname`, cloud is a
per-group constant). The only unique input is "which host am I," which is
intrinsic. That's config *specialisation*, not a pet.

At larger scale the delivery mechanism changes, not the pattern:

| Scale | How config lands |
|---|---|
| a few hosts (here) | a script writes a generated file |
| tens–hundreds | config management (Ansible/Puppet) renders it from inventory |
| hundreds+ | baked into the launch template, or pulled from a config service |
| Kubernetes | a ConfigMap; you never think about nodes |

The weakest part of the current approach is `FLEETDASH_PEERS` — a static list of
"who talks to whom." At scale that's solved with **service discovery**: nodes
register themselves and query a registry. The planned v2 (a Prometheus collector)
moves in that direction.

---

## 11. The architecture split

- dev and stage run on x86-64 VMs.
- production runs on an ARM64 (Graviton) VM.

The lower environments are x86 because that's what the relevant free tiers
provide; there's no ARM host available below production. So the pipeline:

- builds **both** `linux/amd64` and `linux/arm64` every run,
- runs the **tests** on the agent's arch (amd64) only,
- compile-checks and `vet`s the arm64 build but can't run arm64 tests,
- ships the arch-appropriate binary to each environment.

A mixed x86/ARM fleet is realistic — plenty of real estates are mid-migration to
Graviton. The tradeoff (stage doesn't match prod's arch) is real and worth
stating out loud; the mitigation is that pure-Go behaviour is identical across the
two arches, so the compile check catches the overwhelming majority of what could
differ.

---

## 12. What's deliberately not here (v1 scope)

v1 is "get a real continuous-deployment pipeline working end to end." Left for
later, on purpose:

- **State / history.** The dashboard is stateless — it fetches peers live on each
  load. Uptime trending would need a collector + storage; the intended approach is
  a Prometheus `/metrics` endpoint scraped by Prometheus + Grafana on the admin
  box, rather than a bespoke store.
- **TLS.** Plain HTTP over the private mesh. `tailscale serve` can add real certs
  later with no public exposure.
- **A latency grid, an anomaly panel, more nodes.**
- **Provisioning as an Ansible role** instead of a shell script, once the fleet is
  big enough to justify it.
- **Secrets in a real manager** (HCP Vault Secrets / Key Vault) instead of a
  secure file, if an actual app secret ever appears.

---

## 13. Operating it

**Deploy a change:** push to `main`. The pipeline runs; approve prod when
prompted.

**Watch it roll:** open the dashboard on any node. The `version` column changes on
dev, then stage, then prod, as each deploy completes.

**Check health directly:**

```
systemctl status fleetdash
curl -s localhost:8080/api/status
journalctl -u fleetdash -n 50
```

**Roll back:** re-run the pipeline on the previous commit, or on a host,
`cp /opt/fleetdash/fleetdash.prev /opt/fleetdash/fleetdash && systemctl restart fleetdash`.

**Add a node:** run `provision-target.sh` on it with the right `FLEET_*` values,
add it to the other nodes' `FLEETDASH_PEERS`, and (if it's a deploy target) add a
stage to `azure-pipelines.yml` plus a pipeline variable for its hostname.
