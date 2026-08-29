# Machinist

Machinist runs approved commands in Git repositories. A command can be an agent CLI,
test command, shell script, Python program, or repository-owned orchestration script.

```toml
# ~/.machinist/config.toml
[commands.foreman]
executor = "codex"
prompt_file = "prompts/foreman.md" # optional
timeout = "45m"
```

```sh
machinist run --command=foreman --repo=/path/to/repo --prompt="Implement issue 42"
machinist submit --command=foreman --repo=my-repo --prompt="Implement issue 42"
```

The worker owns executor commands and repository paths. The control plane can select only
their configured names, so it cannot submit shell text or machine-local paths. Machinist sends
the rendered prompt on standard input, uses the repository as the working directory, streams
stdout and stderr, records durable artifacts, and applies one overall timeout and cancellation.
Exit code 0 succeeds; every non-zero exit code fails.

Scripts are opaque. Their stages appear only in logs. Machinist does not track child runs,
graphs, checkpoints, or resumable stages. A killed script restarts from the beginning unless
the script owns checkpointing. See [configuration](docs/configuration.md) and the
[workflow examples](examples/workflows/README.md).
