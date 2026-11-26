#!/usr/bin/env bash
# Reference deployment script for building a team config image and deploying
# Machinist's control plane to Google Cloud Run.
#
# Required environment variables:
#   PROJECT_ID       GCP project ID
#   REGION           GCP region (e.g. us-central1)
#   TAG              Immutable image tag (e.g. git SHA or release tag)
#
# Optional environment variables:
#   SERVICE          Cloud Run service name (default: machinist)
#   AR_REPO          Artifact Registry repository name (default: machinist)
#   CLOUDSQL_INSTANCE  Cloud SQL connection name (PROJECT:REGION:INSTANCE);
#                    omit when connecting over TLS to Neon/RDS/Supabase
#   BOOTSTRAP_ADMIN  One-time initial admin (e.g. github:octocat)
#   NO_DEFAULT_URI   Set to 1 after mapping a custom domain to disable *.run.app
#   SECRET_VERSION   Secret Manager version pin (default: latest; pin an integer
#                    in production for strict rollback determinism)

set -euo pipefail

: "${PROJECT_ID:?PROJECT_ID is required}"
: "${REGION:?REGION is required}"
: "${TAG:?TAG is required}"

SERVICE="${SERVICE:-machinist}"
AR_REPO="${AR_REPO:-machinist}"
SECRET_VERSION="${SECRET_VERSION:-latest}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/control-plane:${TAG}"

echo "==> Building and pushing ${IMAGE}"
docker build --platform linux/amd64 -t "${IMAGE}" .
docker push "${IMAGE}"

secrets=(
  "DATABASE_URL=machinist-database-url:${SECRET_VERSION}"
  "GITHUB_OAUTH_CLIENT_ID=machinist-github-oauth-client-id:${SECRET_VERSION}"
  "GITHUB_OAUTH_CLIENT_SECRET=machinist-github-oauth-client-secret:${SECRET_VERSION}"
  "/secrets/github-app.pem=machinist-github-app-key:${SECRET_VERSION}"
)

deploy_args=(
  "--project=${PROJECT_ID}"
  "--image=${IMAGE}"
  "--region=${REGION}"
  "--allow-unauthenticated"
  "--min-instances=1"
  "--max-instances=1"
  "--no-cpu-throttling"
  "--cpu=1"
  "--memory=512Mi"
  "--timeout=120"
  "--ingress=all"
  "--set-secrets=$(IFS=,; echo "${secrets[*]}")"
)

if [[ -n "${CLOUDSQL_INSTANCE:-}" ]]; then
  deploy_args+=("--add-cloudsql-instances=${CLOUDSQL_INSTANCE}")
fi

if [[ -n "${BOOTSTRAP_ADMIN:-}" ]]; then
  deploy_args+=("--set-env-vars=MACHINIST_BOOTSTRAP_ADMIN=${BOOTSTRAP_ADMIN}")
fi

if [[ "${NO_DEFAULT_URI:-0}" == "1" ]]; then
  deploy_args+=("--no-default-uri")
fi

echo "==> Deploying ${SERVICE} to Cloud Run (${REGION})"
gcloud run deploy "${SERVICE}" "${deploy_args[@]}"
