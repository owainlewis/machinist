# Runners

A runner takes jobs from the control plane and runs their steps. Add as many as you like. The
control plane makes no decision based on where a run happens. Each runner needs its own
token and network access to the control plane. It needs no inbound route.

```sh
machinist token create mac-mini --runner
```

A runner's token can take runs and report on its own runs, and nothing else. It cannot sign
in to the web app or use the API. Save the printed token in the file `token_file` names, readable only by the runner's user.

Upgrade the control plane and runners together.

## Local runner

Each step is a process on that machine, in a private workspace, with a private home for each
run and an environment built from an allowlist. Use a dedicated OS account.

```yaml
control_plane: http://10.0.0.5:7331
token_file: .machinist/runner.token
data_directory: .machinist/runner
slots: 2
labels: [macos, arm64]   # optional; target this host with runs_on
env: [ANTHROPIC_API_KEY]
```

`slots` is how many jobs it runs at once. Set `GH_TOKEN` to a token for the bot, then:

```sh
machinist runner check --repo owner/name
```

```sh
machinist runner start
```

`runner check` runs one step that reaches the repository with the runner's token. On Linux,
use `deploy/systemd/machinist-runner.service` so that a crashed runner's steps stop with it.

### What steps can use

A step gets `PATH`, `LANG`, `TMPDIR`, the proxy and CA variables, `GH_TOKEN`, the variables
Machinist sets (see [Factory files](factory.md#what-a-step-gets)) and the variables named in
`env`. Nothing else from the runner's environment reaches a step. The runner refuses to
start when a variable in `env` is unset.

`HOME` is a private directory that is removed after the job. With `login: true`, steps get
the OS user's own `HOME`, `USER` and `LOGNAME` instead, so a tool such as Claude Code can use
a login made on that machine. See [Credentials](credentials.md).

### Which jobs a runner takes

When a local runner starts, it looks for `agy`, `claude`, `codex` and `pi` on its `PATH`.
It takes only jobs whose preset's agents all use a harness it found, so a job that needs
Antigravity waits for a runner that has it instead of failing on one that doesn't. An agent
with its own `command` is not checked. Install a harness and restart the runner for it to
count.

A Docker runner probes its image at startup for `agy`, `claude`, `codex` and `pi` (unless
`docker.harnesses` lists them explicitly), so it reports the image's harnesses on the Runners
page and only takes jobs its image can run.

A Kubernetes runner takes any job whose `runs_on` labels it advertises, because its image decides which harnesses exist.

### Targeting hosts with labels

Set `labels:` in `runner.yaml` to advertise host capabilities such as `macos`, `gpu`, `vpc`, or `k8s`. Labels are trimmed, lowercased, deduplicated, and validated to `[a-z0-9][a-z0-9._-]*`.

```yaml
control_plane: http://10.0.0.5:7331
labels: [macos, arm64, xcode-16]
```

A preset (via `runs_on:` in `factory/presets/*.yaml`) or a job/automation request (via `runs_on` in the API or automation front matter) can require one or more labels. A runner claims a job only when it advertises **every** label in the job's `runs_on` list. Jobs with no `runs_on` match any runner.

The Runners page shows each runner's harnesses and label pills. When no online runner can run a preset,
the Settings overview says which harness or label is missing. Queued jobs that wait for one are
named on the Runners page and on the job's page, with the harnesses or labels they wait for. A
`machinist agent <name>` call in a shell comment does not count.

## Docker runner

Run each job in an ephemeral, hardened container on a single Linux or macOS host, without
Kubernetes. Add a `docker:` block to `runner.yaml` (leaving out `docker:` and `kubernetes:`
keeps the bare-metal local runner unchanged).

```yaml
control_plane: http://10.0.0.5:7331
token_file: .machinist/runner.token
data_directory: .machinist/runner
slots: 2

docker:
  image: ghcr.io/owainlewis/machinist-runner:latest
  runtime: ""            # default vanilla Docker (runc); set "runsc" for gVisor
  network: ""            # optional Docker network (e.g. internal bridge with egress proxy)
  proxy_url: ""          # optional HTTP egress proxy URL
  memory: "4g"           # default container memory limit
  cpus: 2                # default CPU limit
  pids_limit: 512        # prevent fork bombs
  env: [ANTHROPIC_API_KEY]
  # harnesses: [claude, codex]  # optional; probed from the image when omitted
```

### Hardened defaults

Every Docker run starts a fresh container with:

- `--rm` so containers are removed as soon as the run finishes
- non-root `--user` matching the runner user (or `1000:1000` if started as root)
- `--read-only` root filesystem
- `--cap-drop=ALL` and `--security-opt=no-new-privileges`
- `--pids-limit` (default `512`), `--memory` (default `4g`), and `--cpus` (default `2`)
- isolated per-run mounts for workspace, control directory, home, `/tmp`, and exchange (never
  mounting the host user's home directory)
- secret values (`GH_TOKEN` and `env` variables) passed through process environment (`-e NAME`),
  never on the command line or in `request.json`

Step output and progress stream live to the control plane as `events.jsonl` is written inside
the run's mounted exchange directory, just like the local runner. Cancelling a job or hitting
the run timeout runs `docker rm -f` so no orphan container remains.

Docker runners do not support `login: true`.

### 1-line gVisor upgrade (`runtime: runsc`)

By default, Docker uses its standard OCI runtime (`runc`). On Linux, Machinist logs a note at
startup when `runtime` is unset to remind operators that `runc` is active and `runtime: runsc`
is available for untrusted repositories.

To run containers under gVisor:

1. Install `runsc` and register it in `/etc/docker/daemon.json` (`runsc install && systemctl reload docker`).
2. Set one line in `runner.yaml`:

   ```yaml
   docker:
     image: ghcr.io/owainlewis/machinist-runner:latest
     runtime: runsc
   ```

`machinist runner check` and `machinist runner start` verify via `docker info` that Docker
reports the configured runtime installed before taking work, and report `Docker (runsc)` on
the Runners page (or `Docker` with the default runtime).

### Checking and starting

Set `GH_TOKEN` (unless the control plane uses a GitHub App) and any variables listed in `env`,
then run:

```sh
machinist runner check --repo owner/name --config runner.yaml
```

```sh
machinist runner start --config runner.yaml
```

To restrict outbound traffic, attach the container to an internal Docker bridge with `network:`
and point `proxy_url:` at an HTTP CONNECT egress proxy.

## Kubernetes runner

The runner is a process in the cluster that starts one gVisor Job per run, streaming
activity while it runs.

```yaml
control_plane: http://machinist-control-plane.machinist-system.svc:7341
token_file: /etc/machinist/token/token
data_directory: /var/lib/machinist-runner
slots: 4

kubernetes:
  context: my-cluster
  namespace: machinist-runners
  image: "registry.example/runner@sha256:..."
  secret: machinist-agent
  proxy_url: http://machinist-egress:3128
  # allowed_hosts: [registry.example.com]
```

The cluster needs a `gvisor` RuntimeClass and a CNI that enforces NetworkPolicy.

1.  Install the namespace, network policies and egress proxy:

    ```sh
    machinist kubernetes install --config runner.yaml
    ```

2.  Create the one Secret that each Job reads. It must hold `GH_TOKEN`, and every other
    variable in it reaches the steps. `env` is not used. The runner process never reads
    the Secret's values.

    ```sh
    kubectl -n machinist-runners create secret generic machinist-agent \
      --from-literal=GH_TOKEN=... --from-literal=ANTHROPIC_API_KEY=...
    ```

3.  Run the same check, then start the runner:

    ```sh
    machinist runner check --repo owner/name --config runner.yaml
    ```

Kubernetes runners have no `login` option. To allow more hosts, such as a private package
registry, add them to `allowed_hosts` and run `kubernetes install` again.

### Running the runner in the cluster

`deploy/kubernetes/runner-daemon` holds a Deployment, its RBAC and its `runner.yaml`. It uses
the control plane image. Before first use, apply the overlay and create these Secrets:

| Secret | Namespace | Holds |
| --- | --- | --- |
| `machinist-github` | `machinist-system` | `GH_TOKEN` for the control plane |
| `machinist-runner-token` | `machinist-system` | `token`, from `machinist token create <name> --runner` |
| `machinist-agent` | `machinist-runners` | `GH_TOKEN` and the variables steps need |

## Watching the fleet

The Runners page lists every runner that has asked for work, with where it runs, the
agent harnesses it has, how many of its slots are busy and the runs it holds. It also says how many
runs are waiting for a free slot and how long the oldest has waited. A runner that has
made no contact for 90 seconds shows as not responding. Revoking a runner's token removes
it from the list.

A runner taken out of service, such as an old laptop, stays listed as not responding. Remove
it from the Runners page, or with `DELETE /api/v1/runners/{name}`, once it holds no running
job. Removing a runner that is online or still holds a run answers `409 Conflict`. A removed
runner that asks for work again comes back.

## When a runner is lost

A run has a 10-minute lease, renewed every 30 seconds. If a runner dies, the lease expires,
the run is marked interrupted, and the job starts again from the first step on any runner.
Until then the old steps can overlap with the new run. Git then rejects the push from
whichever falls behind. Unpushed work is lost.
