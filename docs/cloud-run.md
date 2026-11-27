# Cloud Run Deployment Guide

This guide deploys Machinist's control plane as a stateless container on **Google Cloud Run** backed by **PostgreSQL** (Cloud SQL, Neon, RDS, or self-hosted), with automatic HTTPS, GCP Secret Manager, GitHub OAuth, and external dial-out runners.

## Architecture Overview

*   **Control plane on Cloud Run (`--min-instances=1 --max-instances=1 --no-cpu-throttling`):** Serves the web UI, API, MCP endpoint, runner poll/heartbeat/events endpoints, and background engine loops.
*   **PostgreSQL (`DATABASE_URL`):** Persistent storage for jobs, runs, tokens, users, invites, sessions, and `leader_leases`. SQLite is refused on Cloud Run (`K_SERVICE` set) so a misconfigured secret can never silently fall back to an ephemeral local database.
*   **Rolling deploy safety:** Cloud Run's `--max-instances=1` is per revision, so rolling deploys briefly overlap Revision `N-1` and Revision `N`. Machinist coordinates this automatically:
    *   Default PostgreSQL pool is `MaxOpenConns=8` (`2 × 8 = 16` connections during overlap, fitting comfortably inside Cloud SQL `db-f1-micro`; override with `DATABASE_MAX_OPEN_CONNS` `1..100`).
    *   `leader_leases` guarantees only one revision runs periodic background loops (`engine.Run`, `schedule.Run`, `runLinear`, `runAuthPrune`) at a time, with `<2s` handoff on clean `SIGTERM` shutdown.
    *   Startup migrations run inside a single transaction-scoped `pg_advisory_xact_lock` with `lock_timeout = 5s` before binding `$PORT`.
*   **External dial-out runners:** Agent runners stay on dedicated compute (Hetzner/GCE VM, Docker + gVisor, or Kubernetes) and poll Cloud Run over HTTPS using a typed `--runner` bearer token.

---

## 1. Provision PostgreSQL

Choose either **Cloud SQL for PostgreSQL** or an external managed PostgreSQL (Neon, RDS, Supabase):

### Option A: Cloud SQL (Unix socket via `--add-cloudsql-instances`)

```bash
gcloud sql instances create machinist-pg \
  --database-version=POSTGRES_16 \
  --tier=db-f1-micro \
  --region="${REGION}"

gcloud sql databases create machinist --instance=machinist-pg
gcloud sql users create machinist --instance=machinist-pg --password="REPLACE_WITH_STRONG_PASSWORD"
```

Your `DATABASE_URL` secret uses the Cloud SQL Unix socket format (note the empty host before `/machinist`):

```text
postgres://machinist:REPLACE_WITH_STRONG_PASSWORD@/machinist?host=/cloudsql/PROJECT_ID:REGION:machinist-pg
```

### Option B: External Managed PostgreSQL (Neon / RDS / Supabase)

Machinist configures `pgx/v5` with `QueryExecModeExec`, so it works out of the box behind **PgBouncer / Neon transaction poolers** without prepared-statement collisions:

```text
postgresql://machinist:REPLACE_WITH_STRONG_PASSWORD@db.example.com:5432/machinist?sslmode=require
```

---

## 2. Create GitHub OAuth App & GitHub App

1.  **GitHub OAuth App (human sign-in):**
    *   In GitHub Settings → Developer settings → OAuth Apps → **New OAuth App**.
    *   **Homepage URL:** `https://machinist.example.com`
    *   **Authorization callback URL:** `https://machinist.example.com/auth/github/callback`
    *   Save the **Client ID** and generate a **Client Secret**.
2.  **GitHub App (repository tokens & PR automation):**
    *   See [GitHub App setup](github-app.md) to create and install your Machinist GitHub App and download its `.pem` private key.

---

## 3. Store Secrets in GCP Secret Manager

Never bake credentials into `config.yaml` or container images. Store each secret in GCP Secret Manager and grant your Cloud Run service account `roles/secretmanager.secretAccessor`:

```bash
printf '%s' "${DATABASE_URL}" | gcloud secrets create machinist-database-url --data-file=-
printf '%s' "${GITHUB_OAUTH_CLIENT_ID}" | gcloud secrets create machinist-github-oauth-client-id --data-file=-
printf '%s' "${GITHUB_OAUTH_CLIENT_SECRET}" | gcloud secrets create machinist-github-oauth-client-secret --data-file=-
gcloud secrets create machinist-github-app-key --data-file=/path/to/github-app.pem
```

Optional secrets:
*   **Webhook notifications:** `NOTIFY_WEBHOOK_URL` (e.g. Slack/Discord incoming webhook URL).
*   **Linear integration:** `LINEAR_CLIENT_ID` and `LINEAR_CLIENT_SECRET` (or `LINEAR_API_TOKEN`).

| Secret / Env | Cloud Run Flag | Required? |
| --- | --- | --- |
| `DATABASE_URL` | `--set-secrets=DATABASE_URL=machinist-database-url:1` | Yes |
| `GITHUB_OAUTH_CLIENT_ID` | `--set-secrets=GITHUB_OAUTH_CLIENT_ID=machinist-github-oauth-client-id:1` | Yes (GitHub OAuth) |
| `GITHUB_OAUTH_CLIENT_SECRET` | `--set-secrets=GITHUB_OAUTH_CLIENT_SECRET=machinist-github-oauth-client-secret:1` | Yes (GitHub OAuth) |
| GitHub App private key | `--set-secrets=/secrets/github-app.pem=machinist-github-app-key:1` | Recommended (or `GH_TOKEN`) |
| `NOTIFY_WEBHOOK_URL` | `--set-secrets=NOTIFY_WEBHOOK_URL=machinist-notify-webhook:1` | Optional |
| `LINEAR_CLIENT_ID` / `LINEAR_CLIENT_SECRET` | `--set-secrets=...` | Optional |
| `MACHINIST_BOOTSTRAP_ADMIN` | `--set-env-vars=MACHINIST_BOOTSTRAP_ADMIN=github:<login>` | First deploy only (inert after first login) |

> Pin integer secret versions (e.g. `:1`) in production for deterministic rollbacks.

---

## 4. Build Your Baked-Config Image & Deploy

Cloud Run deploys from **GCP Artifact Registry**. Create a small team config repository containing `config.yaml`, `factory/`, and a `Dockerfile` based on [`deploy/cloudrun/Dockerfile.example`](../deploy/cloudrun/Dockerfile.example) and [`deploy/cloudrun/config.example.yaml`](../deploy/cloudrun/config.example.yaml):

```dockerfile
FROM ghcr.io/owainlewis/machinist:v0.6.0
COPY --chmod=0644 config.yaml /srv/machinist/config.yaml
COPY --chmod=0755 factory/ /srv/machinist/factory/
```

Create an Artifact Registry repository once:

```bash
gcloud artifacts repositories create machinist \
  --repository-format=docker \
  --location="${REGION}"
gcloud auth configure-docker "${REGION}-docker.pkg.dev"
```

Then deploy using [`deploy/cloudrun/deploy.sh`](../deploy/cloudrun/deploy.sh):

```bash
export PROJECT_ID="my-gcp-project"
export REGION="us-central1"
export TAG="$(git rev-parse --short HEAD)"
export CLOUDSQL_INSTANCE="${PROJECT_ID}:${REGION}:machinist-pg"   # omit for Neon/RDS
export BOOTSTRAP_ADMIN="github:your-github-login"

./deploy/cloudrun/deploy.sh
```

### Custom Domains & `--no-default-uri`

1.  Map your custom domain (`machinist.example.com`) via Cloud Run domain mappings or an External HTTPS Load Balancer (set `server.trusted_proxy_hops: 2` if placing a Global HTTPS LB / Cloud Armor in front of Cloud Run; leave `1` for direct Cloud Run GFE).
2.  Once the custom domain is active, redeploy with `NO_DEFAULT_URI=1` (`--no-default-uri`) so API bearer traffic is not also reachable on the default `*.run.app` hostname.

---

## 5. Day-1 Bootstrap & Connecting Runners

1.  Open `https://machinist.example.com` and click **Continue with GitHub** using the `MACHINIST_BOOTSTRAP_ADMIN` GitHub account. Machinist atomically provisions you as the initial **Admin** and marks bootstrap complete forever.
2.  **Invite teammates:** Go to **Settings → Team** to create targeted GitHub-login or email invites (`admin`, `member`, or `viewer`).
3.  **Connect runners:** Go to **Settings → Tokens**, create a **Runner** token, and configure your external runner (VM, Docker, or Kubernetes) with:
    ```yaml
    control_plane: https://machinist.example.com
    ```
    Runners dial out to Cloud Run over HTTPS — no inbound ports or SSH tunnels are needed on your runner hosts.

### Direct CLI Administration

`machinist user`, `machinist invite`, and `machinist token` connect directly to PostgreSQL when `DATABASE_URL` is set in your environment (e.g. via Cloud SQL Auth Proxy or a one-off Cloud Run Job):

```bash
DATABASE_URL="postgres://..." machinist user list
DATABASE_URL="postgres://..." machinist invite create --github alice --role member
```

`machinist reload` is VM-only. On Cloud Run, commit changes to `config.yaml` or `factory/`, build a new image tag, and deploy a new revision.
