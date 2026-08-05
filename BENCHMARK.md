# New API Benchmarks

This document records live, sequential streaming benchmarks of the Cortex New
API deployment. Credentials are never stored here.

## Method

- Endpoint: `POST /v1/chat/completions`
- Client location: Shanghai, using the same macOS host for both regions
- Input: `echo hi`
- `stream: true`, `max_tokens: 20`
- Five sequential requests per model
- TTFT: elapsed time until the first non-empty SSE response body chunk
- E2E: elapsed time until the stream closes
- Percentiles use nearest-rank over the five samples

The TTFT metric is transport TTFT, not the time to the first semantic text
delta. It is appropriate for comparing the same gateway protocol and client
implementation across regions.

## NYC Baseline

Date: 2026-08-05

Runtime configuration at measurement:

- App Platform service in NYC
- New York PostgreSQL and Valkey
- `MEMORY_CACHE_ENABLED=true`
- Redis pool size 50
- PostgreSQL pool: 25 idle, 50 open, 300 second lifetime
- Batch quota updates enabled with a five second interval
- Relay idle connection reuse: 500 total and 100 per host

| Model | Samples (TTFT ms) | TTFT p50 / p95 | Samples (E2E ms) | E2E p50 / p95 | Success |
| --- | --- | --- | --- | --- | --- |
| `claude-sonnet-5` | 2502, 1817, 1764, 2088, 1689 | 1817 / 2502 | 2656, 2093, 2052, 2367, 1923 | 2093 / 2656 | 5/5 |
| `gpt-5.6-luna` | 1985, 3612, 3380, 2782, 3135 | 3135 / 3612 | 2014, 3612, 3380, 2782, 3157 | 3157 / 3612 | 5/5 |

There is no pre-tuning baseline: the test token was supplied after the NYC
runtime tuning deployment had already completed.

## Singapore Comparison

Date: 2026-08-06

Deployed topology:

- App Platform service and Valkey in `sgp1`
- Existing PostgreSQL primary remains in `nyc3`
- The `sgp1` and `nyc3` VPCs are peered; PostgreSQL is reached over the private
  network
- NYC remains `NODE_TYPE=master`; SG is `NODE_TYPE=slave`, so only NYC owns
  schema migrations and singleton background tasks
- PostgreSQL permits the SG App's exact VPC egress address, not the full SG VPC
  CIDR
- NYC and SG use the same rotated `SESSION_SECRET`; its value is managed in App
  Platform and is intentionally absent from the repository spec

Deployment verification:

- App Platform deployment reached `ACTIVE`
- `/api/status` returned HTTP 200 with `success: true`
- The SG process became ready in 6.3 seconds
- Startup logs confirmed Redis was enabled and the memory cache loaded
- Startup logs contained no `database migration started` entry

The initial master-node attempts were intentionally rejected: cross-region
`AutoMigrate` was still running after more than six minutes and exceeded the
health-check window. Regional request-serving replicas must remain slave nodes;
increasing the health delay is not a substitute for single-owner migrations.

| Model | Samples (TTFT ms) | TTFT p50 / p95 | Samples (E2E ms) | E2E p50 / p95 | Success |
| --- | --- | --- | --- | --- | --- |
| `claude-sonnet-5` | 5642, 2690, 3321, 2804, 2988 | 2988 / 5642 | 6864, 4390, 4527, 4520, 4634 | 4527 / 6864 | 5/5 |
| `gpt-5.6-luna` | 5754, 1969, 3084, 2541, 3743 | 3084 / 5754 | 7096, 3502, 4260, 4034, 5009 | 4260 / 7096 | 5/5 |

Compared with the NYC baseline, Claude TTFT p50 increased from 1817 ms to
2988 ms and E2E p50 increased from 2093 ms to 4527 ms. Luna TTFT p50 was
effectively unchanged (3135 ms versus 3084 ms), while E2E p50 increased from
3157 ms to 4260 ms. Both SG p95 results were dominated by a slow first sample.

This intentionally measures the trade-off of local application/cache access
against cross-region PostgreSQL reads and quota writes. Five sequential samples
are useful as a smoke comparison, not as a capacity test or a statistically
stable latency distribution.
