# Ordered multi-review script

Copy `multi-review.sh` into the repository that Machinist will run, for example as
`scripts/machinist-multi-review.sh`. Add that repository-relative executable to the
worker-owned `worker.toml`:

The agent CLIs use the worker's host access and credentials. This example refuses to run
unless the first prompt line is exactly `trusted-head: yes`, supplied by the operator. A
quoted, embedded, or case-altered value is rejected. It is not a safe way to review
untrusted code.

```toml
[executors.multi-review-script]
command = ["./scripts/machinist-multi-review.sh"]
```

Run the example with both paths made explicit:

```sh
machinist run \
  --machinist-config=/path/to/machinist/examples/workflows/multi-review/config.toml \
  --command=multi-review \
  --repo=/path/to/repo \
  --prompt="trusted-head: yes
Review https://github.com/owner/repo/pull/123"
```

The two stages are visible only in logs. Failure, timeout, or cancellation stops the script;
a later run starts from the beginning unless the script adds checkpointing.
