# Spec 3: Cloud Run Packaging, Runtime Hardening & Production Guide

*   **Author:** Owain Lewis
*   **Status:** Approved (Goldfish-reviewed)
*   **Date:** 2026-11-26
*   **Issue:** ENG-215
*   **Parent architecture:** [Production Cloud Run & PostgreSQL](production-cloud-run.md)
*   **Prerequisites:** [Spec 1: Identity and Access Control](identity-and-access.md) *(completed)*, [Spec 2: PostgreSQL Storage Backend](postgresql-storage.md)

--------------------------------------------------------------------------------

## Overview

With **Spec 1** (GitHub OAuth, DB-managed invites, 3-role RBAC, public control-plane hardening) and **Spec 2** (PostgreSQL storage, expand-and-contract migrations, `leader_leases`) in place, Machinist's control plane can run as a stateless container on **Google Cloud Run** with automatic HTTPS, GCP Secret Manager, and immutable GitOps config images.

This specification keeps packaging minimal, secure, and strictly backwards-compatible with existing VM and Kubernetes deployments:
1. **Cloud Run runtime conventions in Go:**
   *   Honor `$PORT` when `server.listen` is omitted **and** running on Cloud Run (`K_SERVICE != ""`) or with `server.public_url` set (preventing accidental `0.0.0.0:$PORT` exposure on local laptops that happen to have `PORT` set).
   *   Add `NOTIFY_WEBHOOK_URL` environment variable override for `notify.webhook` (and mark `notify.webhook` restart-only in `reload.go` without logging its URL).
   *   Handle Cloud Run's **10-second `SIGTERM` shutdown window** cleanly by running `httpServer.Shutdown(6s)` and `leaderCtx` cancellation **in parallel**, setting `WaitDelay = 2s` on `gh` subprocesses, and releasing `leader_leases` (1.5s timeout) **only** after background loops exit cleanly.
   *   Set aligned HTTP server timeouts (`ReadHeaderTimeout: 5s`, `ReadTimeout: 30s`, `WriteTimeout: 120s`, `IdleTimeout: 60s`) matching Cloud Run `--timeout=120`.
   *   Bound concurrent GitHub lookups inside `github_cache.go` **after** the 30s cache-hit fast path (`max 4` concurrent lookups) and cap process-wide `gh` subprocesses (`max 6`) inside `internal/ghclient/client.go` so large repo lists cannot OOM a 512MiB container.
2. **Official lean container image, `.dockerignore`, & GHCR publishing:**
   *   Add root `.dockerignore` so local `.git`, `.env*`, `.machinist/`, `bin/`, `*.db*`, and `node_modules/` never enter Docker build contexts.
   *   Add `deploy/cloudrun/Dockerfile` using a builder stage to build `machinist` AND download/SHA-256-verify `gh` (`set -o pipefail`), leaving only `ca-certificates`, `/usr/local/bin/gh`, and `/usr/local/bin/machinist` in the final image (zero `curl`, zero `kubectl`), with `/srv/machinist` owned by `root:root` (`0755`, truly read-only to `USER 1000:1000`) and `HOME=/tmp/machinist-home GH_CONFIG_DIR=/tmp/gh`.
   *   Publish multi-arch (`linux/amd64`, `linux/arm64`) images to `ghcr.io/owainlewis/machinist:<tag>` in `.github/workflows/release.yml` (SHA-pinned Docker actions, `needs: build`, `if: github.event_name == 'push'`, scoped `packages: write`, passing `VERSION=${{ env.RELEASE_VERSION }}`).
   *   Provide `deploy/cloudrun/Dockerfile.example`, `deploy/cloudrun/config.example.yaml`, `deploy/cloudrun/deploy.sh`, and automated packaging safety tests (`deploy/cloudrun/test_cloudrun_packaging.py`) in CI.
3. **Production Operations Guide (`docs/cloud-run.md`):**
   *   End-to-end guide covering Cloud SQL (`--add-cloudsql-instances` Unix socket) vs Neon/RDS (`sslmode=require`), Secret Manager mounts (`/secrets/github-app.pem`), Day-1 Web UI bootstrap vs direct-DB CLI admin, GitHub OAuth App setup, custom domains + `--no-default-uri`, and connecting external dial-out runners.

--------------------------------------------------------------------------------

## 1. Runtime & Config Changes

### 1. Safe `$PORT` Support (`internal/config/config.go`, `internal/controlplane/server.go`)
Cloud Run injects `PORT` (default `8080`) and requires binding `:$PORT`.
*   In `applyServerDefaults(server Server, getenv func(string) string)`:
    *   Read `port := strings.TrimSpace(getenv("PORT"))` and `cloudRun := strings.TrimSpace(getenv("K_SERVICE")) != ""`.
    *   When `server.Listen == ""` and `port != ""` and `(cloudRun || server.PublicURL != "")`:
        *   Validate `port` is a decimal integer in `1..65535` (return a clear startup error if invalid — never silently fall back).
        *   Set `server.Listen = ":" + port`.
    *   Otherwise when `server.Listen == ""`: keep `server.Listen = defaultListen` (`127.0.0.1:7331`).
    *   Explicit `server.listen` in `config.yaml` always wins.
*   **Fail-closed Cloud Run & non-loopback check (`validateAuthStartup` in `internal/controlplane/server.go`):**
    *   If `os.Getenv("K_SERVICE") != ""` and `!config.IsPostgresURL(cfg.Server.Database)` → refuse startup (prevents silent ephemeral SQLite fallback).
    *   If `os.Getenv("K_SERVICE") != ""` and `cfg.Server.PublicURL == ""` and `!cfg.Auth.AllowTokenOnlyPublic` → refuse startup (prevents accidentally exposing token-only unhardened auth on Cloud Run).

### 2. `NOTIFY_WEBHOOK_URL` Env Override (`internal/config/config.go`, `internal/controlplane/reload.go`)
*   Add `const NotifyWebhookEnv = "NOTIFY_WEBHOOK_URL"`.
*   In `applyNotifyDefaults(notify Notify, getenv func(string) string)`: if `NOTIFY_WEBHOOK_URL` is non-empty, it overrides `notify.Webhook` and is validated as `http://` or `https://` with non-empty host.
*   In `internal/controlplane/reload.go`: add `notify.webhook` to `restartSettingsDiff` (reporting `"notify.webhook"` only — never printing the secret URL) and preserve `effectiveConfig.Notify = prev.config.Notify` on reload.

### 3. Parallel 10-Second `SIGTERM` Shutdown & Aligned HTTP Timeouts (`internal/controlplane/server.go`, `internal/ghclient/client.go`)
Cloud Run sends `SIGTERM` and hard-kills (`SIGKILL`) after **10 seconds**.
*   **Parallel shutdown sequence (worst-case `<= 9.5s < 10s`):**
    1. At `t = 0` (`ctx.Done()`): cancel `leaderCtx` (stopping background loops) **and** start `httpServer.Shutdown` (`httpShutdownTimeout = 6 * time.Second`) **simultaneously**.
    2. Set `command.WaitDelay = 2 * time.Second` on `gh` subprocesses in `internal/ghclient/client.go` so cancelled `gh` calls terminate promptly instead of hanging on open pipes.
    3. After `httpServer.Shutdown` completes, allow up to `backgroundShutdownTimeout = 2 * time.Second` more for background goroutines to finish.
    4. **Safe lease release:** If all background goroutines exited cleanly, call `ReleaseLease` (with a `1.5s` timeout) so the incoming Cloud Run revision takes over within `<2s`. If any background goroutine is still stuck at deadline, log a warning and **skip `ReleaseLease`** (letting the 45s TTL protect against split-brain).
*   **Aligned HTTP timeouts:**
    *   `ReadHeaderTimeout: 5 * time.Second`
    *   `ReadTimeout: 30 * time.Second`
    *   `WriteTimeout: 120 * time.Second` *(aligned with Cloud Run `--timeout=120` — gives headroom for `POST /api/v1/runs/{id}/complete` PR/review checks and `GET /api/v1/settings/github?refresh=1` without premature TCP drops; Machinist has no long-lived SSE/WebSocket routes).*
    *   `IdleTimeout: 60 * time.Second`

### 4. Bounded GitHub Lookup & `gh` Subprocess Concurrency
*   In `internal/controlplane/github_cache.go`: add `githubSem chan struct{}` (`cap = 4`, initialized in `controlplane.New`, nil-safe in tests) acquired **after** the 30s `githubCache` hit check and immediately before live `app.Identity`, `app.Installed`, or `s.client.RepositoryAccess` calls (selecting on `ctx.Done()` so cancelled HTTP requests bail immediately).
*   In `internal/ghclient/client.go`: add a process-wide subprocess semaphore (`cap = 6`) in `Client.run` (respecting `ctx.Done()`) so combined HTTP + engine + completion load can never OOM a 512MiB container.

### 5. Boot Ordering & Health Probe Contract
*   Startup sequence: `config.LoadConfig` → `factory.Load` → `tickets.Open` (10s ping + advisory-locked migrations) → `controlplane.New` (fail-closed auth/DB checks) → `net.Listen` → serve `/healthz`.
*   Because migrations finish **before** binding `$PORT`, Cloud Run's HTTP startup probe on `GET /healthz` never routes traffic to an unmigrated revision. `/healthz` intentionally omits a live DB ping (avoiding liveness restart storms during transient DB failovers) and returns `{"ok":true}` when `server.public_url` is non-loopback.

--------------------------------------------------------------------------------

## 2. Container Packaging, `.dockerignore` & GHCR Publishing

### Root `.dockerignore`
Exclude `.git`, `.env*`, `.machinist`, `bin`, `target`, `*.db*`, `error.log`, and `internal/controlplane/web/node_modules` (keeping tracked `internal/controlplane/web/dist`) so local secrets and databases never enter Docker build contexts.

### Official Cloud Run Base Image (`deploy/cloudrun/Dockerfile`)
Distinct from `deploy/kubernetes/control-plane.Dockerfile` (which bundles `kubectl` and `procps` for in-cluster Kubernetes runners). `ghcr.io/owainlewis/machinist:<tag>` is the lean control-plane image:
*   **Builder stage (`golang:1.26.6-bookworm`):** compiles static `CGO_ENABLED=0` `machinist` binary AND downloads + SHA-256-verifies `gh` (`GH_VERSION=2.102.0`, `set -o pipefail`).
*   **Runtime stage (`debian:bookworm-slim`):** installs **only** `ca-certificates` (no `curl`, no `kubectl`, no `git`), copies `/usr/local/bin/machinist` and `/usr/local/bin/gh`, creates non-root user `machinist` (UID `1000`), leaves `/srv/machinist` owned by `root:root` (`0755` — **genuinely read-only** to UID `1000`), sets `ENV HOME=/tmp/machinist-home GH_CONFIG_DIR=/tmp/gh GIT_TERMINAL_PROMPT=0`, `USER 1000:1000`, `WORKDIR /srv/machinist`, `EXPOSE 8080`, `ENTRYPOINT ["machinist"]`, `CMD ["start", "--config=/srv/machinist/config.yaml"]`.

### Team Baked-Config Image (`deploy/cloudrun/Dockerfile.example` & `config.example.yaml`)
Cloud Run deploys from **GCP Artifact Registry** (not directly from GHCR). A team's config repo layers `config.yaml` and `factory/` onto the versioned GHCR base image (owned `root:root`, read-only at runtime) and pushes to Artifact Registry:
```dockerfile
FROM ghcr.io/owainlewis/machinist:v0.6.0
COPY --chmod=0644 config.yaml /srv/machinist/config.yaml
COPY --chmod=0755 factory/ /srv/machinist/factory/
```

Minimal schema-valid `deploy/cloudrun/config.example.yaml` (zero secrets baked in):
```yaml
server:
  # Omit server.listen (uses Cloud Run's $PORT automatically) and server.database (uses DATABASE_URL secret).
  public_url: https://machinist.example.com
  trusted_proxy_hops: 1  # 1 = Cloud Run GFE; 2 = Global HTTPS LB / Cloud Armor + Cloud Run

auth:
  # Bootstrap the first admin via MACHINIST_BOOTSTRAP_ADMIN env var; inert forever after first login.
  github:
    enabled: true  # Reads GITHUB_OAUTH_CLIENT_ID & GITHUB_OAUTH_CLIENT_SECRET from Secret Manager

github:
  handle: machinist-bot
  app:
    id: 123456
    private_key_file: /secrets/github-app.pem  # Mounted read-only from Secret Manager
```

### Automated GHCR Publish (`.github/workflows/release.yml`)
Add a `publish-image` job gated on `needs: build` and `if: github.event_name == 'push'` (preserving `workflow_dispatch` dry-runs), scoped with `permissions: { contents: read, packages: write }`, using full **40-char SHA-pinned** `docker/setup-qemu-action`, `docker/setup-buildx-action`, `docker/login-action`, and `docker/build-push-action` building `deploy/cloudrun/Dockerfile` for `linux/amd64,linux/arm64` with `build-args: VERSION=${{ env.RELEASE_VERSION }}`.

--------------------------------------------------------------------------------

## 3. Secrets Matrix, Cloud Run Deploy & Day-1 Bootstrap

All secrets enter via **GCP Secret Manager** (a leading `/` in `--set-secrets` mounts a read-only file instead of an env var; pin secret versions instead of `:latest` for strict rollback determinism):

| Secret / Env | Cloud Run Flag | Required? |
| --- | --- | --- |
| `DATABASE_URL` | `--set-secrets=DATABASE_URL=machinist-database-url:1` | Yes (PostgreSQL) |
| `GITHUB_OAUTH_CLIENT_ID` | `--set-secrets=GITHUB_OAUTH_CLIENT_ID=machinist-github-oauth-client-id:1` | Yes (GitHub OAuth) |
| `GITHUB_OAUTH_CLIENT_SECRET` | `--set-secrets=GITHUB_OAUTH_CLIENT_SECRET=machinist-github-oauth-client-secret:1` | Yes (GitHub OAuth) |
| GitHub App private key | `--set-secrets=/secrets/github-app.pem=machinist-github-app-key:1` | Required unless `GH_TOKEN` secret is supplied |
| `LINEAR_CLIENT_ID` / `LINEAR_CLIENT_SECRET` | `--set-secrets=...` | Optional (if `linear:` enabled) |
| `NOTIFY_WEBHOOK_URL` | `--set-secrets=NOTIFY_WEBHOOK_URL=machinist-notify-webhook:1` | Optional |
| `MACHINIST_BOOTSTRAP_ADMIN` | `--set-env-vars=MACHINIST_BOOTSTRAP_ADMIN=github:<your-github-login>` | One-time initial admin (inert after first login) |

### Reference Deploy Flow (`deploy/cloudrun/deploy.sh`)
`deploy.sh` builds the team's `Dockerfile` (layering `config.yaml` + `factory/` onto `ghcr.io/owainlewis/machinist:<version>`), pushes to GCP Artifact Registry, and deploys to Cloud Run:
```bash
gcloud run deploy machinist \
  --image="${REGION}-docker.pkg.dev/${PROJECT_ID}/machinist/control-plane:${TAG}" \
  --region="${REGION}" \
  --allow-unauthenticated \
  --min-instances=1 \
  --max-instances=1 \
  --no-cpu-throttling \
  --cpu=1 \
  --memory=512Mi \
  --timeout=120 \
  --ingress=all \
  --add-cloudsql-instances="${CLOUDSQL_INSTANCE}" \
  --set-env-vars="MACHINIST_BOOTSTRAP_ADMIN=${BOOTSTRAP_ADMIN}" \
  --set-secrets="DATABASE_URL=machinist-database-url:latest,GITHUB_OAUTH_CLIENT_ID=machinist-github-oauth-client-id:latest,GITHUB_OAUTH_CLIENT_SECRET=machinist-github-oauth-client-secret:latest,/secrets/github-app.pem=machinist-github-app-key:latest"
```
*(Omit `--add-cloudsql-instances` when connecting over TLS to Neon/RDS/Supabase. Once a custom domain is mapped, add `--no-default-uri` so bearer API traffic is not also exposed on the `*.run.app` alias. Note that `--max-instances=1` is per revision: Cloud Run briefly runs 2 instances during rolling deploys, which Spec 2's `MaxOpenConns=8` and `leader_leases` handle cleanly.)*

### Day-1 Bootstrap & CLI Admin Model
1. Deploy with a pre-planned custom domain in `server.public_url` (or deploy once to learn the `.run.app` URL, update `public_url` + GitHub OAuth callback URL, and redeploy).
2. Open `https://machinist.example.com` and click **Continue with GitHub** as `MACHINIST_BOOTSTRAP_ADMIN` → provisioned as initial **Admin**.
3. Create runner tokens in **Settings → Tokens** (copy onto your Hetzner/GCE/GKE runner) and invite teammates in **Settings → Team** — **zero SSH or DB tunnels needed**.
4. Direct CLI administration (`machinist user`, `machinist invite`, `machinist token`) connects directly to PostgreSQL via `DATABASE_URL` (using Cloud SQL Auth Proxy or a one-off Cloud Run Job if the DB is private). `machinist reload` is VM-only; on Cloud Run, commit config/factory changes and deploy a new revision.

--------------------------------------------------------------------------------

## 4. Vertical Implementation Slices

| Slice | Scope | Done when |
| --- | --- | --- |
| **Slice 3.1: Cloud Run Runtime Hardening** | Safe `$PORT` fallback in `config.go` (`K_SERVICE != "" || public_url != ""` + strict port validation), Cloud Run `K_SERVICE` fail-closed guards in `server.go`, `NOTIFY_WEBHOOK_URL` env override + `reload.go` restart-only handling, parallel `SIGTERM` shutdown (`6s` HTTP + `2s` drain + conditional `1.5s` `ReleaseLease`) + `WriteTimeout=120s` + `gh` `WaitDelay=2s`, `githubSem` (cap 4, post-cache) in `github_cache.go`, and process-wide `gh` semaphore (cap 6) in `ghclient.Client`. | `go test -race ./...` passes with tests proving `$PORT` safety, `K_SERVICE` fail-closed guards, `NOTIFY_WEBHOOK_URL` override/reload, bounded `gh` concurrency, and clean parallel shutdown. |
| **Slice 3.2: Container Packaging, GHCR Release & `docs/cloud-run.md`** | Add root `.dockerignore`, `deploy/cloudrun/Dockerfile`, `Dockerfile.example`, `config.example.yaml`, `deploy.sh`, and `test_cloudrun_packaging.py` (wired into `ci.yml`); Go test validating `config.example.yaml` via `config.LoadConfig`; add SHA-pinned multi-arch `publish-image` job to `.github/workflows/release.yml`; write `docs/cloud-run.md` and link from `docs/README.md`. | CI validates `test_cloudrun_packaging.py`, `config.example.yaml` parses cleanly via `LoadConfig`, and `docs/cloud-run.md` is complete and accurate. |
