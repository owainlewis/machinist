# Ordered multi-review script

Copy `multi-review.sh` into the repository that Machinist will run, for example as
`scripts/machinist-multi-review.sh`. Add that repository-relative executable to the
worker-owned `worker.toml`:

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
  --prompt="Review PR 123"
```

The two stages are visible only in logs. Failure, timeout, or cancellation stops the script;
a later run starts from the beginning unless the script adds checkpointing.
