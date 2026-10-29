# GKE control plane

Run one Machinist control plane and its built-in Kubernetes dispatcher in the
existing runner cluster. This deployment uses SQLite on a 20 GiB persistent
disk, one replica, and the Recreate update strategy. Do not scale it beyond one
replica. No external database, scheduler, service-account key, or personal gcloud
login is needed at runtime.

The dispatcher uses the Pod's rotating Kubernetes service-account token through
an explicit `in-cluster` kubeconfig. Its Role is limited to the runner namespace:
Jobs, Pod inspection and exec, credential-stream ConfigMaps, the existing
`codex-account` Secret, and read-only inspection of the proxy and isolation
policies. Its only cluster-scoped permission reads the `gvisor` RuntimeClass.
It cannot install policies or change IAM. Agent Pods still receive no Kubernetes
token, GitHub credential, or control-plane credential.

## Build and render

From the repository root, build and push the control-plane image. This image
contains Git, GitHub CLI and kubectl, but no coding agent or Google Cloud CLI. The embedded UI must
already be built; `web/dist` is committed alongside its source.

```sh
docker buildx build --platform linux/amd64 \
  --file deploy/kubernetes/control-plane.Dockerfile \
  --build-arg VERSION="$(git rev-parse --short HEAD)" \
  --tag "$CONTROL_PLANE_IMAGE" --push .
```

Set `CONTROL_PLANE_IMAGE` to your registry's image reference before building.
Deploy using its resolved digest, not a mutable tag. Create a local Kustomize
overlay referencing `deploy/kubernetes/control-plane` and replace the image named
`machinist-control-plane` with that digest. Render with `kubectl kustomize` and
validate with server-side dry-run after creating the `machinist-system` namespace.
The runner namespace, policies, egress proxy and Codex Secret must already exist.

The checked-in `deploy/kubernetes/control-plane-gke` overlay pins the deployed
image for `machinist-prod`, `europe-west2`, cluster `machinist-runners`. For updates after
initial state migration, apply it with the explicit cluster context:

```sh
kubectl --context gke_machinist-prod_europe-west2_machinist-runners \
  apply -k deploy/kubernetes/control-plane-gke
```

Update the overlay's digest after building and verifying a replacement image.

## Cut over existing state

1. Confirm no agent attempts are executing. Gracefully stop the local control
   plane and check that its runner Jobs and credential-stream locks have been
   cleaned up. Keep the original runtime directory as a private rollback copy.
2. On a fresh cluster, provision the namespace, persistent claim and dispatcher
   resources with **zero control-plane replicas** before transferring state:

   ```sh
   kubectl --context gke_machinist-prod_europe-west2_machinist-runners \
     apply -k deploy/kubernetes/control-plane-bootstrap
   ```

   This bootstrap overlay reuses the GKE definition and overrides only the
   replica count. Do not apply the normal one-replica overlay yet. The temporary
   transfer Pod binds and mounts the claim before the control plane starts.
3. With the old process stopped, take a SQLite backup of `jobs.db` and preserve
   its runtime directory, including onboarding settings, GitHub credentials,
   checkouts, worker outputs, automation definitions, and publication holds.
   Do not put this private archive in an image, ConfigMap, Git repository, or
   build context. Transfer it over authenticated Kubernetes exec to a temporary
   non-root Pod mounting `machinist-state`, then remove that Pod.
4. Use `/var/lib/machinist` as the new runtime root. Update onboarding repository
   paths and repair Git worktree administrative paths for the moved checkouts.
   Keep the database's reports and events unchanged. Do not release private
   publication holds. Keep setup unfinished during migration so no jobs admit
   before validation.
5. Place `config.toml`, `worker.token` and referenced prompt files in the runtime
   root. Set `server.listen` to `127.0.0.1:7341`, `server.state` to `factory`, and
   `server.worker_token_file` to `/var/lib/machinist/worker.token`. Configure the
   existing runner image and credential Secret under `server.kubernetes`, using
   `context = "in-cluster"` and `kubeconfig = "/etc/machinist/kubeconfig"`.
   Preserve the existing runner namespace and proxy URL.
6. After the transfer Pod is removed and the configuration is in place, apply
   `deploy/kubernetes/control-plane-gke` to start the single replica. Use the UI to finish the existing Kubernetes setup.
   Run its real connection check before enabling schedules. Confirm history,
   projects, automations and holds survived. Restart the Deployment and repeat
   the check to verify persistence and service-account access.

For CI-gated main-branch deployments, see [continuous deployment](continuous-deployment.md).

## Watch it live

View the dashboard and jobs from any machine with gcloud and kubectl installed
and an account with access to the cluster:

```sh
gcloud auth login
gcloud container clusters get-credentials machinist-runners --region europe-west2 --project machinist-prod
kubectl -n machinist-system port-forward deploy/machinist-control-plane 7341:7341
```

Keep the port-forward running and open http://127.0.0.1:7341.

The default deployment creates no Service, public IP, ingress or load balancer.
It retains Machinist's loopback listener and denies Pod ingress. Authorized
operators access it through the Kubernetes API. This operator tunnel uses your
Kubernetes identity; the running dispatcher does not. Reconnect the tunnel after
replacing the Pod. The dashboard has no login of its own, so keep it behind
port-forward and never expose it publicly.

In another terminal, watch agent pods with k9s or kubectl:

```sh
k9s -n machinist-runners
# Or:
kubectl -n machinist-runners get pods -w
```

## Recovery and updates

For an existing deployment, use this update sequence:

1. Pause every scheduled automation and stop manual/API submission clients for
   the entire maintenance window. Record which schedules were enabled so only
   those are resumed afterwards. Finish or cancel active work, then gracefully
   stop the control-plane owner so GitHub follow-up polling cannot enqueue repairs:

   ```sh
   kubectl --context "$CLUSTER_CONTEXT" -n machinist-system \
     scale deployment/machinist-control-plane --replicas=0
   kubectl --context "$CLUSTER_CONTEXT" -n machinist-system \
     wait --for=delete pod --selector=app=machinist-control-plane --timeout=180s
   ```

   Confirm agent Jobs, Pods and credential locks are also gone. Do not force-delete
   unreachable workloads. Keep the owner stopped until the backup is complete.
2. Make a private, consistent SQLite backup plus a runtime archive. A raw copy of
   `jobs.db` while SQLite is writing is not a consistent backup; use SQLite's
   backup API with the owner stopped. Mount the claim in a temporary trusted,
   non-root utility Pod to copy the private runtime; do not start Machinist there.
   Remove that Pod before restarting the control plane. Protect archives as
   credentials and never add them to the build context.
3. Build from reviewed source, verify `machinist version` inside the image, push
   it, verify the registry digest, and review the overlay's digest change.
4. Run server-side validation, apply the normal GKE overlay, and wait for rollout:

   ```sh
   kubectl --context "$CLUSTER_CONTEXT" apply --dry-run=server \
     -k deploy/kubernetes/control-plane-gke
   kubectl --context "$CLUSTER_CONTEXT" apply -k deploy/kubernetes/control-plane-gke
   kubectl --context "$CLUSTER_CONTEXT" -n machinist-system \
     rollout status deployment/machinist-control-plane --timeout=180s
   ```

5. Reconnect port-forward to the replacement Pod. Confirm one healthy worker,
   preserved job IDs, projects, automations, GitHub connection and publication
   holds. Run a real connection check or narrow job and verify runner cleanup.
   Only after validation, resume the previously enabled schedules and submission
   clients. Leave schedules that were already paused unchanged.

Never reapply the zero-replica bootstrap overlay during an ordinary update.
Never restore the old laptop runtime over newer cloud history.

Keep the old image digest and matching private state backup for rollback. Recreate stops the old dispatcher before a replacement starts;
the process lock also prevents concurrent access to the store. Allow its 90-second
termination grace period for bounded sandbox cleanup. Do not force-delete an
unreachable dispatcher or clear a credential lock until its old agent Pods are
confirmed stopped. Follow the credential recovery procedure in
[Kubernetes runners](kubernetes.md).

For rollback to the laptop, gracefully stop the cloud Deployment first and copy
back its latest private state. Never start both copies. Reusing the original
pre-cutover directory after cloud activity would discard newer history and could
repeat scheduled work.

The PVC survives Deployment replacement and deletion. It is persistent storage,
not a backup; deleting the PVC can delete its disk. Keep periodic private SQLite
backups and runtime archives separately. This first deployment has no automatic
backup service or public endpoint.

## Availability limits

The `standard-rwo` claim is zonal. A replacement control-plane Pod needs capacity
in the disk's zone; healthy nodes in another zone cannot mount that claim. Node
recovery or a capacity failure can therefore leave the app unavailable even when
the cluster API is healthy. Recreate and a persistent disk do not provide
cross-zone failover. Keep private backups outside the active runtime and plan
recovery before treating this deployment as highly available.

If Pods remain Pending, inspect scheduling events and disk node affinity before
changing authentication or retrying agent work. Do not force-delete an unreachable
Pod or clear credential locks to bypass uncertain process ownership. Follow
[Google's Autopilot troubleshooting guide](https://docs.cloud.google.com/kubernetes-engine/docs/troubleshooting/autopilot-clusters)
for node and capacity failures. A snapshot still marked `CREATING` is not a
completed recovery backup.

## Verified deployment

Live checks on 2026-10-18 verified the digest-pinned image on the existing
`machinist-runners` GKE cluster. A sandbox connection check returned PONG, verified
GitHub and agent access, and removed its Job, Pod and credential-stream lock.
Replacing the control-plane Pod preserved all 19 existing tasks, four automations,
the GitHub connection and the private publication hold. Its built-in dispatcher
reconnected automatically using the Kubernetes service account.

The deployed GitHub CLI authenticated and fetched paginated check results for an
existing PR. RBAC checks allowed required dispatch operations and denied listing
Secrets, changing policies, creating role bindings, or creating Jobs in another
namespace. The runtime image contains no Google Cloud CLI or personal cloud login.
