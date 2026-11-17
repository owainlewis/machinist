# Runners

A runner takes jobs from the control plane and runs their steps. Add as many as you like. The
control plane makes no decision based on where a run happens. Each runner needs its own
token and network access to the control plane. It needs no inbound route.

```sh
machinist token create mac-mini
```

Save the printed token in the file `token_file` names, readable only by the runner's user.

Upgrade the control plane and runners together.

## Local runner

Each step is a process on that machine, in a private workspace, with a private home for each
run and an environment built from an allowlist. Use a dedicated OS account.

```toml
control_plane = "http://10.0.0.5:7331"
token_file = "~/.machinist/runner.token"
data_directory = "~/.machinist/runner"
slots = 2
env = ["ANTHROPIC_API_KEY"]
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

`HOME` is a private directory that is removed after the job. With `login = true`, steps get
the OS user's own `HOME`, `USER` and `LOGNAME` instead, so a tool such as Claude Code can use
a login made on that machine. See [Credentials](credentials.md).

## Kubernetes runner

The runner is a process in the cluster that starts one gVisor Job per job run. Activity
appears when the run ends.

```toml
control_plane = "http://machinist-control-plane.machinist-system.svc:7341"
token_file = "/etc/machinist/token/token"
data_directory = "/var/lib/machinist-runner"
slots = 4

[kubernetes]
context = "my-cluster"
namespace = "machinist-runners"
image = "registry.example/runner@sha256:..."
secret = "machinist-agent"
proxy_url = "http://machinist-egress:3128"
# allowed_hosts = ["registry.example.com"]
```

The cluster needs a `gvisor` RuntimeClass and a CNI that enforces NetworkPolicy.

1.  Install the namespace, network policies and egress proxy:

    ```sh
    machinist kubernetes install --config runner.toml
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
    machinist runner check --repo owner/name --config runner.toml
    ```

Kubernetes runners have no `login` option. To allow more hosts, such as a private package
registry, add them to `allowed_hosts` and run `kubernetes install` again.

### Running the runner in the cluster

`deploy/kubernetes/runner-daemon` holds a Deployment, its RBAC and its `runner.toml`. It uses
the control plane image. Before first use, apply the overlay and create these Secrets:

| Secret | Namespace | Holds |
| --- | --- | --- |
| `machinist-github` | `machinist-system` | `GH_TOKEN` for the control plane |
| `machinist-runner-token` | `machinist-system` | `token`, from `machinist token create` |
| `machinist-agent` | `machinist-runners` | `GH_TOKEN` and the variables steps need |

## Watching the fleet

The Runners page lists every runner that has asked for work, with where it runs, how
many of its slots are busy and the runs it holds. It also says how many
runs are waiting for a free slot and how long the oldest has waited. A runner that has
made no contact for 90 seconds shows as not responding. Revoking a runner's token removes
it from the list.

## When a runner is lost

A run has a 10-minute lease, renewed every 30 seconds. If a runner dies, the lease expires,
the run is marked interrupted, and the job starts again from the first step on any runner.
Until then the old steps can overlap with the new run. Git then rejects the push from
whichever falls behind. Unpushed work is lost.
