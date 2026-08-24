# Codex on Cloud Run smoke test

This experiment runs one prompt through the Codex CLI in a disposable Cloud
Run Job. It is the smallest test of the path:

```text
operator -> Cloud Run Job -> Codex CLI -> OpenAI API -> Cloud Logging
```

The existing [`../cloud-run-agent`](../cloud-run-agent/README.md) experiment is
the more complete Factory proof of concept. It runs Pi through OpenRouter,
checks out a frozen repository commit, and preserves verified result artifacts
in GCS. This folder deliberately does less so the Codex and Cloud Run setup can
be tested independently.

## Security boundary

The operator and prompt are trusted. The prompt must not contain secrets. Cloud
Run execution overrides are visible to project members who can inspect the Job.
The OpenAI API key is mounted from Secret Manager as a file and is added to the
environment only for the Codex process. A model-issued shell command can inherit
that environment, so the read-only sandbox is not a secret-isolation boundary.

The smoke test runs Codex with a read-only sandbox and no repository. Do not
reuse this setup for untrusted repository code. Before running repository
scripts, replace the long-lived API key with OpenAI workload identity
federation or another credential-isolating design.

API-key runs use OpenAI Platform billing rather than ChatGPT subscription
credits.

## Local checks

Run the focused runner test and shell syntax checks:

```sh
bash -n experiments/cloud-run-codex/*.sh
experiments/cloud-run-codex/test-run-codex.sh
```

Build the same Linux image that Cloud Run will use:

```sh
docker build --platform linux/amd64 \
  --tag factory-codex-cloud-run-smoke \
  experiments/cloud-run-codex
```

To make a real local model call without placing the key in the container
environment:

```sh
key_file="$(mktemp)"
trap 'rm -f "$key_file"' EXIT
printf '%s' "$OPENAI_API_KEY" > "$key_file"

docker run --rm --platform linux/amd64 \
  --env 'PROMPT=Reply with exactly: hello from local Docker' \
  --mount "type=bind,source=${key_file},target=/secrets/openai/api-key,readonly" \
  factory-codex-cloud-run-smoke
```

## Deploy to the Factory Google Cloud project

The existing Factory project ID is `factory-505220`. This experiment uses
resource names distinct from the Pi and OpenRouter proof of concept.

Set the deployment values:

```sh
export PROJECT_ID=factory-505220
export REGION=europe-west1
export REPOSITORY=experiments
export IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPOSITORY}/codex-agent-smoke:0.149.1"
export SERVICE_ACCOUNT="codex-agent-smoke@${PROJECT_ID}.iam.gserviceaccount.com"
export SECRET_NAME=openai-codex-api-key
export JOB_NAME=codex-agent-smoke
```

Enable the required APIs:

```sh
gcloud services enable \
  artifactregistry.googleapis.com \
  cloudbuild.googleapis.com \
  run.googleapis.com \
  secretmanager.googleapis.com \
  --project "$PROJECT_ID"
```

The existing `experiments` Artifact Registry repository can be reused. Create
it only if it does not exist:

```sh
gcloud artifacts repositories describe "$REPOSITORY" \
  --location "$REGION" \
  --project "$PROJECT_ID" >/dev/null 2>&1 || \
gcloud artifacts repositories create "$REPOSITORY" \
  --repository-format docker \
  --location "$REGION" \
  --project "$PROJECT_ID"
```

Build and publish the image:

```sh
gcloud builds submit experiments/cloud-run-codex \
  --tag "$IMAGE" \
  --project "$PROJECT_ID"
```

Create the OpenAI Platform API key secret without putting its value in shell
history:

```sh
printf '%s' "$OPENAI_API_KEY" | \
  gcloud secrets create "$SECRET_NAME" \
    --data-file=- \
    --replication-policy=automatic \
    --project "$PROJECT_ID"
```

If the secret already exists, add a new version instead:

```sh
printf '%s' "$OPENAI_API_KEY" | \
  gcloud secrets versions add "$SECRET_NAME" \
    --data-file=- \
    --project "$PROJECT_ID"
```

Create a dedicated service account and grant access to only that secret:

```sh
gcloud iam service-accounts describe "$SERVICE_ACCOUNT" \
  --project "$PROJECT_ID" >/dev/null 2>&1 || \
gcloud iam service-accounts create codex-agent-smoke \
  --display-name 'Codex Cloud Run smoke test' \
  --project "$PROJECT_ID"

gcloud secrets add-iam-policy-binding "$SECRET_NAME" \
  --member "serviceAccount:${SERVICE_ACCOUNT}" \
  --role roles/secretmanager.secretAccessor \
  --project "$PROJECT_ID"
```

Create or update the reusable Job:

```sh
gcloud run jobs deploy "$JOB_NAME" \
  --image "$IMAGE" \
  --region "$REGION" \
  --project "$PROJECT_ID" \
  --service-account "$SERVICE_ACCOUNT" \
  --set-secrets "/secrets/openai/api-key=${SECRET_NAME}:latest" \
  --tasks 1 \
  --max-retries 0 \
  --task-timeout 30m \
  --cpu 2 \
  --memory 2Gi
```

## Run a prompt

Use a non-sensitive prompt for the first execution:

```sh
gcloud run jobs execute "$JOB_NAME" \
  --region "$REGION" \
  --project "$PROJECT_ID" \
  --update-env-vars 'PROMPT=Reply with exactly: hello from Codex on Cloud Run' \
  --wait
```

Read the JSONL output from Cloud Logging:

```sh
gcloud run jobs logs read "$JOB_NAME" \
  --region "$REGION" \
  --project "$PROJECT_ID" \
  --freshness 10m \
  --order asc
```

The final response appears in an `item.completed` event whose item type is
`agent_message`.

## Deliberate limits

- One manually supplied prompt per execution.
- Read-only Codex sandbox.
- No repository checkout or persisted filesystem.
- No result store other than Cloud Logging.
- No Factory Task, Run, Session, retry, or cancellation integration.
