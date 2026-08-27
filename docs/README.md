# Machinist documentation

- [Getting started](getting-started.md): install Machinist and run the shipped
  foreman and audit agents
- [How Machinist works](how-it-works.md): understand execution modes, supervision,
  artifacts, and exit behavior
- [Configuration](configuration.md): define agents, executors, models, prompts,
  pipelines, and managed repositories
- [Local control plane](control-plane.md): run the server and worker, and
  understand their security and recovery boundaries
- [Managed triggers](managed-triggers.md): start jobs from GitHub labels,
  intervals, and cron schedules
- [Development](development.md): build, test, and navigate the repository
- [Migration guide](migration-from-factory.md): clean installation, renamed
  interfaces, and rollback
- [Architecture](../ARCHITECTURE.md): source of truth, dependency direction,
  execution flows, trust boundaries, and persistence
- [Control-plane design](control-plane/design.md): read the detailed V1 design,
  protocol, invariants, and acceptance criteria
- [Warp Factories product review](product-direction/warp-factories-review.md):
  compare the products and review Machinist's readiness and priorities
- [Runner-managed skills](worker-skills/design.md): understand why coding-agent
  skills stay native to the configured runner
