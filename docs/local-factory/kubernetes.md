# Kubernetes runners

The control plane runs on your computer, VM, or in the
[GKE deployment](cloud-control-plane.md). Its trusted connector launches
one standard Kubernetes Job per attempt. The agent runs in gVisor, commits locally,
and returns a bounded Git bundle. The connector checks that commit graph and
publishes the assigned branch and PR using the existing GitHub App connection.
The agent receives neither GitHub credentials nor a control-plane or Kubernetes
API token. This release supports Codex or Claude Code subscription authentication.

## Prepare a cluster

Use a cluster with a working `gvisor` RuntimeClass and enforced NetworkPolicy.
The tested deployment is GKE Autopilot in `machinist-prod`, `europe-west2`, cluster
`machinist-runners`. The execution code uses ordinary Jobs, Secrets, ConfigMaps,
Deployments and NetworkPolicies. Other providers still need their own gVisor and
network setup and have not been tested.

Build and push `deploy/kubernetes/runner.Dockerfile` to your private registry.
Use the resulting image digest, not a mutable tag. Only your nodes need image-read
access. The image includes Codex, Claude Code, Git, Node, Go and Python. It contains
no login cache, GitHub CLI, cloud credentials or worker token.

Create a worker configuration containing a `[kubernetes]` section:

- `context`: an explicit kubectl context, never the implicit current context.
- `kubeconfig`: optional path to that context's kubeconfig.
- `namespace`: a dedicated runner namespace.
- `image`: your runner image pinned by `@sha256:` digest.
- `auth_secret`: the name of this runner's dedicated agent account Secret.
- `proxy_url`: `http://machinist-egress.<namespace>.svc.cluster.local:3128`.

Then install the namespace and proxy:

```sh
machinist kubernetes install --config worker.toml
kubectl --context YOUR_CONTEXT -n YOUR_NAMESPACE rollout status deployment/machinist-egress
machinist kubernetes login --config worker.toml
```

For Codex, the login command runs native `codex login --device-auth` using a new
private temporary cache. Complete the displayed device authorization yourself. It stores
only that login's `auth.json` in the named Kubernetes Secret, then removes the
temporary directory. It never copies your desktop credential cache. Do not paste
tokens into chat or put them in source control. Existing Secrets are not silently
overwritten.

For Claude Code, set `agent = "claude"` in `[kubernetes]` and use a token from
`claude setup-token`, stored with `machinist kubernetes login`.

Add the same fields under `[server.kubernetes]` in your control-plane config and
restart it. Setup then offers **Kubernetes** as an execution host. Select Codex or
Claude Code and run the real PONG check before finishing setup. Closing the local
control plane does not move its database into the cluster.

## Isolation and delivery

Every attempt uses a fixed digest-pinned template: gVisor, non-root, read-only
root filesystem, dropped capabilities, no privilege escalation, no host mounts
or namespaces, no mounted service-account token, bounded temporary storage and a
deadline. Triage and Review get a read-only source mount. Build and Spec get a
writable source mount. Each uses fresh source, home and temporary volumes.

An init container receives only a Git snapshot and execution request. Before the
agent starts it checks that metadata, Kubernetes API and direct public access are
denied, an unapproved proxy destination is rejected, and the Codex destination is
reachable through the approved proxy. Missing runtime, policies, model login or
proxy readiness fails closed. It never switches to a local agent or API billing.

The proxy accepts CONNECT to exact approved HTTPS hostnames only. It resolves and
pins public addresses and rejects internal addresses. NetworkPolicy limits agent
traffic to that proxy and cluster DNS. The proxy has no model or GitHub credential.
Its destination list is maintained in `internal/kuberunner/install.go`; changing it
requires an operator deployment. Namespace administrators can change policies, so
this is an operator-controlled boundary, not protection from a cluster admin.

Source and returned bundles are limited to 100 MiB; each log is limited to 10 MiB.
The host imports commits into a disposable bare repository, verifies their reported
head and ancestry, and chooses the repository and branch itself. It never checks
out or runs sandbox code in the publisher. Pushes are fast-forward only. PR lookup
precedes creation, including recovery after a lost first publication response.
Both modes wait for a verified merge. Human merge is the default; only qualifying
factory documentation PRs can use the opt-in
[repository maintenance policy](automations.md#repository-maintenance).

## Subscription credentials and failure recovery

The connector's Kubernetes credentials are separate from the Codex subscription
Secret. A local or VM connector using a personal Google Cloud login may need
`gcloud auth login` on that host, using the same `CLOUDSDK_CONFIG` as Machinist.
The GKE control plane instead uses its rotating in-cluster service-account token;
it does not need a personal gcloud login. An expired operator login affects
cluster administration and port-forward access, not the in-cluster dispatcher.
Do not reinstall gVisor or reseed the Codex Secret for a cloud login failure.
Permission errors require checking the host identity's RBAC and cloud IAM;
missing-resource errors identify the policy, RuntimeClass, or Secret to restore.
Command diagnostics use bounded stderr to select safe recovery advice. Raw stderr
is never copied into the run error.

A Kubernetes ConfigMap lock serializes the whole dedicated credential stream,
including across connector processes. The native CLI refreshes its own cache.
The connector reads that updated cache privately and updates the Secret using its
current resource version before acknowledging completion. It unlocks only after
foreground Job deletion and a check that no attempt Pods remain.

Cancellation first asks the running supervisor to stop its native process, retains
the resulting cache when possible, and then stops the Job. Control-plane shutdown
allows up to 50 seconds for that bounded cleanup. There are no automatic Job or
Pod retries. A lost create response looks up the same Job rather than creating
another. Attempt metadata and private logs stay in the local runtime directory.

If the connector or node disappears before auth writeback or stopped-work proof,
the lock remains. Do not delete it just to make a retry run. Inspect the Job named
in that ConfigMap and the run's private `kubernetes.json`, stop the old workload
with foreground deletion, and verify no associated Pods remain. If refreshed auth
could not be recovered, remove the stale account Secret and perform a fresh native
login before explicitly clearing the credential lock. Never force-delete a Pod on
an unreachable node and assume its old process has stopped. Restore connectivity
or fence the node before reusing that account stream.

This is personal, trusted automation. The CLI can read its model credential, as can
code running as that CLI. gVisor does not conceal it from code inside the container.
Use a dedicated login and trusted tasks; this does not provide a shared subscription
service or multiply account limits. The implementation follows native account-auth
cache persistence rather than implementing an OAuth refresh client.

## Verification

Automated checks cover unsafe archive rejection, no API-key fallback, fixed Job
boundaries, proxy denial, cancellation auth retention, delayed Pod deletion,
target routing, restart and shutdown, and lost publication reconciliation. A real
GKE probe verified denied metadata/API/direct internet access, proxy restrictions,
read-only source and absence of a cluster token.

Live acceptance on 2026-10-15 used a dedicated Codex subscription login: the
read-only connection check returned PONG, a fresh single-agent Job returned PONG,
and the standard pipeline triaged a small documentation task, skipped Spec,
built it and published [PR #519](https://github.com/owainlewis/machinist/pull/519).
Independent Review accepted the pushed revision after CI passed. A second commit
on the same PR exercised public Why / What / Verification descriptions without
publishing the private handoff. Local/VM and sandbox delivery share this prompt
contract. The trusted connector also updates the existing PR description.

A cancellation test stopped a running native command, confirmed no attempt Pods
or credential locks remained, and then passed another native PONG check using the
retained login. This proves cache persistence across fresh Pods, including after
cancellation; a new login did not require token refresh during these short tests.

The UI shows live preparation, GitHub verification, sandbox startup, execution and
publication phases with the runner heartbeat and elapsed time. These transient
signals stay in memory and are never published to GitHub. Preparation diagnostics
are bounded private logs retained before Pod cleanup. The checkout is created by
the non-root init container inside the source volume, so Git ownership checks
remain enabled.
