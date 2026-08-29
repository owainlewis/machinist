# Bounded review loop

Copy `review-loop.sh` into the repository that Machinist will run, for example as
`scripts/machinist-review-loop.sh`. Configure the worker-owned executor as
`command = ["./scripts/machinist-review-loop.sh"]`. The same repository must provide
`scripts/wait-for-review.sh` and `scripts/read-review-feedback.sh`.

Run it with the example command configuration:

```sh
machinist run \
  --machinist-config=/path/to/machinist/examples/workflows/review-loop/config.toml \
  --command=review-loop \
  --repo=/path/to/repo \
  --prompt="Implement issue 123 and address review feedback"
```

Machinist sees only the script logs and final exit code. Cancellation or timeout kills the
script process tree. A later run starts from the beginning unless the script implements its
own durable checkpointing.
