# Runners

A runner takes work from the control plane and runs agents. Add as many as you like. The
control plane makes no decision based on where a run happens. Each runner needs its own
token and network access to the control plane. It needs no inbound route.

```sh
machinist token create mac-mini
```

Save the printed token in the file `token_file` names, readable only by the runner's user.

Upgrade the control plane and runners together.

## Local runner

The agent is a process on that machine, with a private home for each run and an environment
built from an allowlist. Use a dedicated OS account.

```toml
control_plane = "http://10.0.0.5:7331"
token_file = "~/.machinist/runner.token"
data_directory = "~/.machinist/runner"
slots = 2

[credentials.claude]
env = ["ANTHROPIC_API_KEY"]
```

`slots` is how many runs it does at once. A runner offers only the harnesses it has a
credential for. Set `GH_TOKEN` to a token for the bot, then:

```sh
machinist runner check --repo owner/name
```

```sh
machinist runner start
```

`runner check` makes one real run per harness, which costs a model call, and prints the
credential source the harness reported. On Linux, use `deploy/systemd/machinist-runner.service`
so that a crashed runner's agents stop with it.

## Kubernetes runner

The runner is a process in the cluster that starts one gVisor Job per run. Activity appears
when the run ends.

```toml
control_plane = "http://machinist-control-plane.machinist-system.svc:7341"
token_file = "/etc/machinist/token/token"
data_directory = "/var/lib/machinist-runner"
slots = 4

[credentials.claude]
env = ["ANTHROPIC_API_KEY"]

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

2.  Create the one Secret that each Job reads. It must hold `GH_TOKEN` and the variables
    named under `credentials`. The runner process never reads its values.

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
| `machinist-agent` | `machinist-runners` | `GH_TOKEN` and the harness credentials |

## When a runner is lost

A run has a 10-minute lease, renewed every 30 seconds. If a runner dies, the lease expires,
the run is marked interrupted, and the ticket continues on any runner. Until then an old
agent can overlap with the new run. Git then rejects the push from whichever falls behind.
Unpushed work is lost, which is why the playbook says to push often.
