# Machinist

Run coding agents on infrastructure you control: your computer, a VM, or
Kubernetes. Connect GitHub, submit a job, and get a pull request or a direct
answer. Machinist is early-access, open-source software.

The Go control plane and React UI track execution, approvals, review and delivery.
Codex or Claude Code runs the work through its native CLI. Agents are reusable
configurations; projects choose the repositories where jobs run.

<a id="quick-start"></a>

## Quick start

Install Go 1.26.6, Git, GitHub CLI and your chosen coding CLI. Authenticate the
coding CLI on its execution host. Then build from the repository root:

```sh
git clone https://github.com/owainlewis/machinist.git
cd machinist
go build -o ./bin/machinist ./cmd/machinist
./bin/machinist init
./bin/machinist start
```

The production UI bundle is committed, so this Go build does not need Node.
Open the printed loopback URL, connect your GitHub App, choose a project and
agent, and verify a real PONG. See the [setup guide](docs/local-factory/README.md).

Add repositories through **Projects → Add project**. Only repositories granted
to the GitHub App appear. **Manage GitHub access** and **Refresh repositories**
let you grant more. Projects share agent settings and the selected execution
target; adding one preserves the existing projects.

## Run a job

Use **Runs → New run** with a request or GitHub issue link:

- **Standard pipeline:** Triage → optional Spec and approval → Build → independent
  Review. Findings return to Build on the same PR, for up to three repair rounds.
- **Single agent:** one agent completes a task end to end, without a separate
  reviewer. Use it for operational work and saved automations.

Requested files, including reports and documents, are delivered through a PR.
Questions and checks without requested changes can return a result directly.
GitHub holds published issues, specs and PRs. Internal prompts, handoffs, logs and
execution state stay in the private Machinist runtime.

Machinist watches delivered PRs for CI failures, trusted review feedback and
confirmed merges. Repairs stay on the same task and PR. You normally decide when
to merge; optional repository maintenance can auto-merge a narrow class of
independently reviewed, low-risk documentation changes under enforced branch
protections. Single-agent and unrelated PRs never qualify.

## Automate repository work

Save an agent job in **Automations**, run it manually, or give it a cron schedule
and timezone. Each run starts at the project root. Missed schedules coalesce into
one run, and unresolved jobs prevent overlap. Keep the control plane running.

**Repository maintenance** triages bounded batches of open issues and PRs,
appends existing classification labels without removing human labels, and applies
the fixed conservative merge policy. It never closes issues or PRs or posts
internal reports to GitHub. Start manually and inspect its report before enabling
a schedule. See [Automations](docs/local-factory/automations.md) for exact rules.

## Choose infrastructure

| Target | Execution and ownership |
| --- | --- |
| Local | Managed native CLI worker on the control-plane host |
| VM | Same local setup on a VM, or a paired remote worker with its own credentials |
| Kubernetes | A gVisor Job per attempt; dedicated native Codex login; trusted connector publishes PRs |

The control plane can also run in GKE: one replica, SQLite on a persistent disk,
and an in-cluster dispatcher. No public ingress or external database is required.
The shipped Kubernetes target supports Codex subscription login; other providers
and billing modes are not implied. There is no required Machinist cloud account.

One control plane owns the private runtime. Back up its full state, including a
consistent SQLite snapshot and worker outputs. Do not run a stale laptop copy
alongside the cloud dispatcher. Publication holds survive restarts and require an
explicit release. See [job recovery](docs/local-factory/local-jobs.md).

## Go deeper

| Guide | What it covers |
| --- | --- |
| [Documentation](docs/README.md) | Current setup, operations, and separate legacy references |
| [Your first job](docs/task-guide.md) | Submission, spec approval, review, and delivery |
| [GitHub follow-ups](docs/local-factory/github-followups.md) | CI failures, review feedback, and merge handling |
| [Configuration](docs/configuration.md) | App settings, worker executors, commands, and compatibility |
| [VM deployment](docs/vm-deployment.md) | Host the app or connect a remote worker |
| [Kubernetes runners](docs/local-factory/kubernetes.md) | gVisor, native login, isolation, and recovery |
| [GKE control plane](docs/local-factory/cloud-control-plane.md) | Deployment, private access, backup, and rollback |
| [Development](docs/development.md) | Build, check, and contribute |

Direct `machinist run` commands and legacy sequence workflows remain available for
scripts. They are separate from the app's two built-in modes; see
[the command reference](docs/workflows.md).

Read [CONTRIBUTING.md](CONTRIBUTING.md) before contributing and
[SECURITY.md](SECURITY.md) for private vulnerability reports. Machinist uses the
[MIT License](LICENSE).
