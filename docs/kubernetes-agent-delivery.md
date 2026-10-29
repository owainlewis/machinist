# Kubernetes agent delivery

Machinist's control plane can run on your computer, a VM, or in the
[GKE cluster](local-factory/cloud-control-plane.md) while Codex or Claude Code runs in
Kubernetes. Each stage runs as one isolated Kubernetes Job.

Triage and Review receive read-only source. Agent pods receive no GitHub
credentials or Kubernetes API credentials. The trusted connector publishes the
task branch and pull request.

Internal execution logs and agent handoffs stay private on the execution host.

Follow [Kubernetes setup and recovery](local-factory/kubernetes.md) for native
login, isolation and credential retention. Merge manually unless the PR qualifies
for the fixed [repository maintenance policy](local-factory/automations.md#repository-maintenance).
Delivery is complete only after a confirmed merge of the recorded head.
