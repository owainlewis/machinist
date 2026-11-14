# Continuous deployment to GKE

The `Deploy GKE` GitHub workflow deploys the current main commit after its push CI
succeeds. Pull requests cannot deploy. A manual workflow run also requires the
current main commit to have passed CI. Superseded revisions are skipped, and
production deployments run one at a time without cancelling an active update.
The workflow must be merged into main before it can run.

The target is the existing `machinist-prod` project, `europe-west2` region and
`machinist-runners` cluster. It updates the control-plane image, the runner Deployment,
the agent image and the egress proxy.

## One-time setup

An operator with the existing project and cluster administration rights runs:

```sh
bash deploy/kubernetes/bootstrap-cd.sh
```

This creates the deployment service account and Workload Identity Federation
provider in a dedicated pool, scoped to this repository's numeric identity, main branch and specific
deployment workflow. It grants image publishing on the Machinist registry,
cluster metadata access and namespace-scoped Kubernetes deployment permissions.
The deployment identity has no direct permission to administer IAM or network
policies, or to read or change the agent Secret. Deployment authority does include
executing code in the application namespace and mounting its private runtime. It
can therefore act through the application’s own credentials and service account.
Protect main and the workflow as production authority.
No service-account key or subscription token is added to GitHub. Bootstrap reruns
restore the provider condition and refuse other providers in the dedicated pool.
The trust uses the standard `workflow_ref` claim described in
[GitHub’s OIDC reference](https://docs.github.com/en/actions/reference/security/oidc).

The `production` environment contains only the provider and service-account
identifiers as variables. To require a human gate, configure environment required
reviewers. Leaving it without reviewers allows continuous deployment after CI.

## Release and backup

Both images are built from the tested commit, checked with `machinist version`,
pushed to Artifact Registry and resolved to SHA-256 digests. Credentials are
requested after building, and generated credential files are excluded from
Docker contexts. Only digest-pinned images are deployed.

`deploy/kubernetes/deploy.py` is used by GitHub and can also be run by an
operator with explicit context, image digests and full source revision. An
operator can add `--snapshot-backend gcloud` to use existing Google Cloud access
for a zonal disk snapshot without installing CD identities, RBAC or a snapshot
class. This mode verifies the snapshot is `READY`; CI uses Kubernetes snapshots.
It:

1. Refuses to start while runs are queued or running, or Jobs remain. Retry when idle.
2. Stops the runner, then the one Recreate control plane, and waits for their Pods. It never
   forces deletion.
3. Mounts the existing PVC in a temporary non-root Pod with no service-account token, and
   makes an integrity-checked SQLite backup under
   `/var/lib/machinist/deployments/<release>/`.
4. Creates a retained disk snapshot of the whole runtime and waits for `readyToUse=true`
   before changing anything.
5. Updates the agent image in the runner's ConfigMap, removes the temporary Pod, and rolls out
   the proxy and control plane.
6. Checks that `/healthz` reports this release and that saved tickets survived, then starts
   the runner.

The workflow summary records source revision, current/previous digests, snapshot
name and preserved ticket count. It does not upload runtime archives or logs.
Snapshots use `Retain` and must be managed separately for cost and retention.
The same-disk SQLite copy is useful for recovery but is not the independent
backup; the completed disk snapshot is.

## First deployment

`deploy.py` only updates images. Before the first release, apply
`deploy/kubernetes/control-plane-gke` once and create the Secrets listed in
[Runners](../runners.md#running-the-runner-in-the-cluster). Put `config.toml` and the factory
directory on the state volume, with `listen` on a Pod address such as `0.0.0.0:7341`.

## Failure and rollback

A deployment that is busy fails before stopping the app. Retry the workflow when
idle. A failure after shutdown leaves the owner stopped rather than automatically
running an older binary against changed state. Inspect the workflow error,
Deployment, temporary Pod and snapshot. Never start a second owner or discard
unpublished work to repair a release.

The snapshot and release record identify the previous image and state. Restoring
an older image without matching state is unsafe after a database migration. Use a
restored PVC when a state rollback is needed. Do not delete the live PVC as a rollback step.

CD patches the live image digests. The checked-in GKE overlay is the bootstrap
baseline, not a continuously updated release ledger. Reapplying that overlay can
roll the app back; use the workflow's verified release digests for later updates.

The existing private access model remains: use an authenticated Kubernetes
port-forward to port 7341. No public ingress is created.
