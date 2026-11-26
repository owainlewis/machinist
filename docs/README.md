# Documentation

Start with the [quick start](../README.md#quick-start).

## Use Machinist

| Guide | Use it to |
| --- | --- |
| [Run API](run-api.md) | Start and follow jobs from any system |
| [MCP](mcp.md) | Let a bot run and watch jobs over Model Context Protocol |
| [Linear](linear.md) | Start a job by assigning a Linear issue to Machinist |
| [Factory files](factory.md) | Write presets, configure merge gate and PR Steward, and add automations |
| [Merge policies](merge-policies.md) | Configure risk-based auto-merge and starter policies |

## Run it

| Guide | Use it to |
| --- | --- |
| [Configuration](configuration.md) | Look up every setting (including live reload, webhooks and PR Steward) |
| [Runners](runners.md) | Add local, Docker (with optional gVisor), and Kubernetes runners |
| [Credentials](credentials.md) | Choose the credentials steps get |
| [GitHub App](github-app.md) | Give each run a short-lived token for its own repository |
| [VM deployment](vm-deployment.md) | Run the control plane and a runner on a VM |
| [Continuous deployment](local-factory/continuous-deployment.md) | Release to GKE |
| [Architecture](../ARCHITECTURE.md) | See how the pieces fit |
| [Security](../SECURITY.md) | Understand what a step can do |

## Build it

| Guide | Use it to |
| --- | --- |
| [Development](development.md) | Build, test and contribute |
| [Vision](../VISION.md) | See what Machinist is for, where it is, and what comes next |
| [Design](designs/generic-runner.md) | Read the design: one API, presets of steps, runners |
| [Identity & access control](designs/identity-and-access.md) | Read Spec 1 (v1): GitHub OAuth, DB invites, 3-role RBAC, and public hardening |
| [PostgreSQL storage](designs/postgresql-storage.md) | Read Spec 2: PostgreSQL backend, lock-free claiming, migrations, and leader leases |
| [Cloud Run packaging](designs/cloud-run-packaging.md) | Read Spec 3: Cloud Run runtime, baked-config container image, and Secret Manager deploy |
| [Production Cloud Run & auth](designs/production-cloud-run.md) | Read the parent architecture for Cloud Run, PostgreSQL, baked config, and public auth |
| [Job sources](designs/issue-sources.md) | See where jobs come from and how a new tracker plugs in |
| [Earlier design](designs/machinist-redesign.md) | Read the GitHub-first design the code still follows |
| [Examples](../examples/README.md) | See the files `machinist init` installs |
| [Issue triage demo](issue-triage-demo.md) | See a standalone GitHub Actions demo |

## History

These record earlier decisions, not current setup: [workflow roadmap](workflow-roadmap.md),
[original factory spec](local-factory/spec.md) and
[Warp observations](local-factory/warp-evidence.md).
