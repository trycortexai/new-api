# New API streaming stress test

This command runs closed-loop Chat Completions stages at concurrency 300, 600,
1200, and so on. Each stage schedules work for three minutes, lets its last
in-flight requests finish, and stops after the first stage below 90% success or
at the configured hard concurrency ceiling.

The API key is accepted only through `NEW_API_KEY`. It is never written to the
artifacts or command output.

```bash
export NEW_API_KEY='...'
go run ./tools/newapi-stress
```

Defaults target `https://newapi.withcortex.ai/v1` with
`fake-agent-medium-v1`. A successful fake response must be HTTP 200, contain a
`cortex-fake-llm` content marker, report exactly 20,000 input and 2,000 output
tokens, have one `finish_reason: stop`, and end with exactly one `[DONE]`. There
are no retries. Before concurrency 300, a mandatory one-request preflight must
pass all of these checks or the run aborts.

To verify authentication, routing, protocol behavior, and fake usage without
starting concurrency 300, run exactly that one request and exit:

```bash
go run ./tools/newapi-stress --preflight-only
```

The preflight-only run still writes and safely finalizes both artifacts.

The default fake mode rejects paid-only controls, so it cannot accidentally send
billed traffic. The same command has a separately gated Sonnet mode described
below.

Each run writes `samples.jsonl` and `summary.json` below a unique directory in
`stress-results/`. The summary includes success rate, status/error counts,
throughput, and mean/p50/p90/p95/p99/max for time to first token and time to
complete. TTFT and completion percentiles include successful requests only;
`all_request_duration` includes successful and failed requests. Latency samples
are exact through 100,000 observations per metric per stage. Above that cap the
summary reports `latency_samples_dropped` instead of growing memory without a
bound. The expected three-minute fake-model stages remain well below the cap.
`scheduling_window_ms` records the actual scheduling window, including when a
signal or artifact failure stops a stage early; it is not copied from the
configured duration. Workers also check the absolute scheduling deadline before
every request, so delayed timer processing cannot start work after the boundary;
requests already in flight are still allowed to drain.

Useful safety overrides:

```bash
go run ./tools/newapi-stress \
  --max-concurrency 1200 \
  --stage-duration 3m \
  --output-dir stress-results
```

The default requires HTTP/2. To run a transport control that cannot negotiate
HTTP/2, select HTTP/1.1 explicitly:

```bash
go run ./tools/newapi-stress --http-version 1.1
```

Use `--http-version 2` (the default) for the HTTP/2 case. Any other value is
rejected before the preflight. The selected protocol is recorded in
`summary.json`, and the protocol negotiated for every request is recorded in
`samples.jsonl`. A request that negotiates a different protocol fails; a
preflight mismatch aborts before load begins. This option does not relax the
fake-model lock, mandatory preflight, concurrency ceiling, timeouts, response
validation, or no-retry policy.

## Explicitly gated paid Sonnet run

Paid mode accepts exactly `claude-sonnet-5` and requires every safety interlock
below. This is the complete command for the approved two-minute, concurrency-300
run:

```bash
go run ./tools/newapi-stress \
  --model claude-sonnet-5 \
  --allow-paid-sonnet \
  --start-concurrency 300 \
  --max-concurrency 300 \
  --stage-duration 2m \
  --max-cost-usd 1 \
  --max-requests 50000
```

Sonnet mode rejects any concurrency other than exactly 300, a non-positive or
greater-than-two-minute stage, a missing or greater-than-$2 cost cap, and a
missing or greater-than-50,000 request cap. The request cap includes the
mandatory preflight. There are no retries. Scheduling stops at either cap; the
request cap is exact, while the cost cap can have at most one already in-flight
wave (300 requests) of overshoot. A paid response without usable usage data
stops scheduling immediately with `unknown_usage`; the hard request cap still
bounds the already in-flight wave. `stop_reason` and per-stage `stopped_by_cap`
identify why scheduling stopped.

Each request uses `max_tokens: 2` and asks the model to respond with exactly
`hi`. A success requires byte-for-byte content `hi`, one or two reported
completion tokens, a `finish_reason` of `stop` or `length`, one usage frame,
exactly one `[DONE]`, and no later SSE events. (`length` is valid because the
two-token request bound may coincide with the end of the exact answer.) The fake
model remains `stop`-only. The runner records prompt, cached-prompt,
uncached-prompt, and completion tokens for the preflight and stage. It estimates
list cost at $2/M uncached input tokens, $0.20/M cached input tokens, and $10/M
output tokens. Cost-cap overshoot remains limited to one already in-flight wave;
the two-token response bound caps that wave's output-token list-cost component
at $0.006, while input-token cost is added from reported usage.

`total_usage` in `summary.json` aggregates the preflight and stage. Failed
requests that reach a valid usage frame are included in the estimate. A failed
request may never return usage even if the provider charges it, so
`unknown_usage_requests` is reported and `estimated_list_cost_usd` explicitly
excludes any such unknown charged usage. The list-cost estimate is not a
guarantee of the gateway's actual billed charge; use the billing ledger as the
source of truth. The hard request cap remains effective regardless of whether
usage is returned.
