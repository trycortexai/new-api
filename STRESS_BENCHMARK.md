# New API streaming load benchmark

Date: 2026-08-07

This document records exploratory single runs against the deployed New API
service. The results are not production SLOs or a capacity guarantee. Repeat the
tests before using any threshold for production sizing.

## Test method

The load generator sent closed-loop streaming Chat Completions requests. A
worker started its next request after the prior request finished. The fake-model
ramp doubled concurrency until a stage fell below the 90% success gate. Stages
stopped scheduling at their configured deadline and then allowed in-flight
requests to drain. There were no retries.

The public target was `https://newapi.withcortex.ai/v1/chat/completions`.
Transport controls used the deployment's direct DigitalOcean hostname, which is
intentionally omitted here. The temporary load generator ran in DigitalOcean
NYC3 on a 4-vCPU, 8-GiB compute-optimized Droplet. The deployed New API service
had two 1-vCPU, 1-GiB replicas. The fake LLM had one 2-vCPU, 4-GiB replica.

For `fake-agent-medium-v1`, success required all of the following:

- HTTP 200 over the selected HTTP protocol.
- Valid server-sent events with at least one content event.
- The expected fake-model content marker.
- Exactly 20,000 input tokens and 2,000 output tokens.
- One `finish_reason: stop` and exactly one `[DONE]`.
- No timeout, truncated body, malformed event, or transport error.

For `claude-sonnet-5`, the request asked for the exact response `hi`. Success
required HTTP 200, content equal to `hi`, one usage frame, one or two output
tokens, a valid terminal finish reason, exactly one `[DONE]`, and no later SSE
events.

Time to first token (TTFT) is measured from request start to the first content
token. Time to complete is measured from request start to the terminal stream
event. Total request duration also includes final body processing and failures.
Latency percentiles below include successful requests unless the column says
otherwise.

## Mac baseline through the public endpoint

These stages ran from a Mac on the office network, before moving the generator
to DigitalOcean. Both used HTTP/2 and scheduled for three minutes.

| Concurrency | Requests | Successes | Success rate | TTFT p50 / p95 / p99 | Complete p50 / p95 / p99 |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 300 | 1,200 | 1,200 | 100.00% | 2.084 / 3.105 / 3.328 s | 51.785 / 54.899 / 55.842 s |
| 600 | 2,400 | 1,900 | 79.17% | 2.091 / 7.304 / 8.282 s | 51.903 / 66.335 / 67.727 s |

All 2,400 c600 requests received HTTP 200. The 500 failures comprised 361
transport errors and 139 timeouts. The timeout diagnostics further separated
100 request timeouts and 39 SSE idle timeouts. New API recorded 355 downstream
client disconnects. The correlated application logs showed another 145 streams
that completed in both applications but did not deliver a terminal event to the
Mac. This run alone did not identify which network layer terminated those
streams.

Moving the generator to DigitalOcean changed the observed failure mode. The Mac
c600 result therefore must not be treated as service capacity.

## DigitalOcean fake-model ramp

The generator ran beside the deployed services in DigitalOcean and used the
public endpoint over HTTP/2. Each stage scheduled for 60 seconds.

| Concurrency | Requests | Successes | Success rate | Statuses | Failure kinds |
| ---: | ---: | ---: | ---: | --- | --- |
| 600 | 1,182 | 1,182 | 100.00% | 1,182 x 200 | none |
| 1,200 | 2,318 | 2,249 | 97.02% | 2,249 x 200; 69 x 504 | 69 HTTP status |
| 2,400 | 4,292 | 2,598 | 60.53% | 3,612 x 200; 319 x 504; 1 x 500 | 320 HTTP status; 101 malformed SSE; 913 timeout; 360 transport |

| Concurrency | TTFT mean / p50 / p95 / p99 / max | Complete mean / p50 / p95 / p99 / max |
| ---: | ---: | ---: |
| 600 | 3.617 / 2.188 / 8.525 / 8.599 / 8.648 s | 53.291 / 51.766 / 58.640 / 58.753 / 58.945 s |
| 1,200 | 3.506 / 2.795 / 7.908 / 12.571 / 16.428 s | 53.276 / 52.492 / 58.786 / 62.275 / 66.028 s |
| 2,400 | 7.107 / 5.798 / 18.717 / 25.073 / 30.269 s | 57.596 / 55.370 / 72.461 / 81.781 / 83.408 s |

The c600 and c1200 stages passed the 90% gate. The c2400 stage failed it, so the
ramp stopped.

### c2400 resource and application evidence

During the public-endpoint c2400 interval:

- New API CPU peaked at 89.96% and 86.14% across its two one-vCPU replicas.
  Memory peaked at 45.86% and 42.18%. No restart or OOM was observed.
- The fake LLM peaked at 1.677 CPU cores out of two and 181.09 MiB RSS. Mean
  scheduler lag was approximately 0.756 ms per event. It was busy but did not
  exhaust memory or both CPU cores.
- The DigitalOcean generator peaked around 39% process CPU and 347 MiB RSS. Its
  HTTP/2 run used at most 25 established TCP connections. The generator was not
  CPU- or memory-bound.
- During the c2400 stage, the fake LLM accepted 4,036 requests, completed 2,984
  successfully, and recorded 1,052 downstream client disconnects. The remaining
  256 attempts did not reach the fake service. The client rejected another 386
  streams that the fake service had recorded as successful.

The New API runtime emitted 2,888 slow-SQL records during the correlated c2400
window:

| Query class | Count | p50 | p95 | Max |
| --- | ---: | ---: | ---: | ---: |
| Consume-log insert | 2,185 | 305 ms | 3.48 s | 55.10 s |
| Subscription-count query | 629 | 277 ms | 3.21 s | 56.92 s |

The live SQL pool configuration was 50 maximum open and 25 maximum idle
connections per New API replica: at most 100 open and 50 idle connections
across the two replicas. Connection lifetime was 300 seconds. Batch updates
were enabled on a five-second interval, and the slow-query threshold was 100 ms.

This evidence confirms severe database latency and high New API CPU at c2400.
It does not prove that the SQL pool limit, the database itself, or ingress was
the first bottleneck. The current data does not include database-side wait-event
or connection-saturation telemetry at sufficient resolution.

## Direct DigitalOcean hostname controls

Both controls used the deployment's default DigitalOcean hostname, c2400, a
60-second scheduling window, and the same fake-model validation. Bypassing the
outer custom-hostname Cloudflare proxy did not restore the success rate.

| Transport | Requests | Successes | Success rate | Statuses | Failure kinds |
| --- | ---: | ---: | ---: | --- | --- |
| HTTP/2 | 4,863 | 2,481 | 51.02% | 3,454 x 200; 760 x 504 | 760 HTTP status; 4 malformed SSE; 969 timeout; 649 transport |
| HTTP/1.1 | 5,081 | 1,963 | 38.63% | 2,843 x 200; 1,643 x 504 | 1,643 HTTP status; 1,475 timeout |

| Transport | TTFT mean / p50 / p95 / p99 / max | Complete mean / p50 / p95 / p99 / max |
| --- | ---: | ---: |
| HTTP/2 | 6.814 / 5.302 / 17.733 / 23.925 / 26.632 s | 56.384 / 54.321 / 69.853 / 70.600 / 76.818 s |
| HTTP/1.1 | 7.795 / 6.222 / 19.275 / 21.303 / 31.533 s | 58.092 / 57.023 / 67.522 / 72.930 / 82.640 s |

The direct HTTP/2 generator peaked at 42.6% process CPU, 333 MiB RSS, and 25
established TCP connections. The HTTP/1.1 generator peaked at 36.1% process CPU,
451 MiB RSS, and 2,400 established TCP connections. Generator resource
exhaustion was not observed in either control.

HTTP/1.1 requests reached Gin as late as 89 seconds after they were scheduled.
Its slow-SQL maxima were 55.55 seconds for consume-log inserts and 107.25
seconds for subscription-count queries. HTTP/1.1 performed worse than HTTP/2,
so HTTP/2 multiplexing is not the sole cause. The default DigitalOcean hostname
also failed over HTTP/2, so the outer custom-hostname Cloudflare proxy is not
required to reproduce the c2400 failure.
Ingress queuing, New API CPU, SQL contention, and pool pressure remain plausible
contributors. The available measurements do not establish their causal order.

## Claude Sonnet 5, c300 for two minutes

The paid run used the public endpoint over HTTP/2 with these safety gates:

- Concurrency fixed at 300.
- Maximum aggregate scheduling time of two minutes.
- `max_tokens: 2` and exact expected content `hi`.
- No retries.
- A $2 estimated-list-cost stop and a 50,000-request hard cap. The 31-second
  continuation used a separate $1.20 cap and a 20,000-request cap.
- Immediate scheduling stop if a completed request lacked usable usage data.

An initial `max_tokens: 1` preflight returned `h`, as expected for a one-token
limit, and failed the exact-content gate. No load stage followed that preflight.
It used 16 input tokens and one output token and cost $0.000042.

The corrected run stopped after 88.941210 seconds when one transport failure
had unknown usage. A second 31.000847-second segment completed the approved
window. The combined stage scheduling time was 119.942057 seconds.

| Metric | Combined result |
| --- | ---: |
| Requests | 18,988 |
| Successes | 18,987 |
| Failures | 1 transport failure with unknown usage |
| Success rate | 99.9947335% |
| Peak concurrency | 300 |
| Input tokens | 303,792 |
| Output tokens | 37,974 |
| Stage list cost | $0.987324 |

Combined latency across both stage segments:

| Metric | Mean | p50 | p90 | p95 | p99 | Max |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| TTFT | 1,769.450 ms | 1,602.101 ms | 2,478.489 ms | 2,888.546 ms | 3,510.723 ms | 10,846.358 ms |
| Complete | 1,818.632 ms | 1,653.906 ms | 2,544.359 ms | 2,940.620 ms | 3,559.655 ms | 10,848.667 ms |
| Total request duration | 1,923.526 ms | 1,756.135 ms | 2,675.449 ms | 3,071.052 ms | 3,760.103 ms | 20,000.705 ms |

The verified price was $2 per million uncached input tokens, $0.20 per million
cached input tokens, and $10 per million output tokens. No cached input tokens
were reported. The stage calculation was:

```text
(303,792 * $2 / 1,000,000) + (37,974 * $10 / 1,000,000)
= $0.607584 + $0.379740
= $0.987324
```

The two successful preflights added $0.000104, giving an observed/list cost of
$0.987428 for the successful two-segment test. Including the rejected
`max_tokens: 1` calibration request, total observed spend for the Sonnet session
was $0.987470. The failed stage request returned no usage; a provider could
charge unknown usage even when the stream does not return it. The gateway
billing observation matched the reported-usage list calculation for the known
usage recorded here.

## Confirmed findings and limits

Confirmed by these runs:

- Moving c600 from the Mac/office path to the DigitalOcean generator changed
  success from 79.17% to 100%.
- The DigitalOcean public-endpoint fake ramp passed c1200 at 97.02% and failed
  c2400 at 60.53%.
- Default-host HTTP/2 and HTTP/1.1 both failed c2400, and HTTP/1.1 was worse.
- At c2400, New API CPU was high and its runtime recorded multi-second SQL tail
  latency. Fake-LLM and generator memory were not exhausted, and no application
  restart or OOM was observed.
- The two-minute c300 Sonnet stage achieved 99.9947335% success for a known
  reported-usage list cost of $0.987324.

Not proven by these runs:

- A stable production capacity boundary. Each result is one short run.
- That the database pool limit is the first bottleneck.
- That DigitalOcean ingress, New API CPU, SQL contention, or a single upstream
  resource independently caused the c2400 failure.
- The charge, if any, for the one failed Sonnet request with missing usage.

## Artifacts

These `/tmp` paths are operator references on the machine that ran the test.
They are not durable repository storage. SHA-256 values cover the files as
recorded on 2026-08-07.

| Run | Artifact | SHA-256 |
| --- | --- | --- |
| Mac fake ramp | `/tmp/newapi-fake-ramp.xLDDRX/fake-ramp-20260807T082045Z/summary.json` | `aa0e95c783b2952dc9f7eeefeeee38467da18f0e7c8cebd28c93c288e69c65d6` |
| Mac fake ramp | `/tmp/newapi-fake-ramp.xLDDRX/fake-ramp-20260807T082045Z/samples.jsonl` | `275288fa219453f60f0b30010457dcd28cf7bfdab3997bc157af27443f4c71bc` |
| DO fake ramp | `/tmp/codex-newapi-loadgen.kr0a55/verified-artifacts/do-ramp-20260807T0900Z/summary.json` | `994640350521dd5f7346f60446238196d0b060672156aa48973d9b58471e8f35` |
| DO fake ramp | `/tmp/codex-newapi-loadgen.kr0a55/verified-artifacts/do-ramp-20260807T0900Z/samples.jsonl` | `64ea3668c7c2bbc216b325e8c5050b2733268e2254f47ea03c7099907274cb49` |
| DO fake ramp | `/tmp/codex-newapi-loadgen.kr0a55/verified-artifacts/do-ramp-20260807T0900Z/generator-metrics.csv` | `0c09d3b7caeb9172834ec6aa7470eda602a8f6500b74a6ae70261412dd64df4d` |
| Direct HTTP/2 | `/tmp/codex-newapi-loadgen.kr0a55/verified-origin-control/do-origin-h2-c2400-20260807T0915Z/summary.json` | `a0bdb07ae781bce0df1d3273bbeb3f77f9ce2a67a80ac9494b5d6f2217777b0f` |
| Direct HTTP/2 | `/tmp/codex-newapi-loadgen.kr0a55/verified-origin-control/do-origin-h2-c2400-20260807T0915Z/samples.jsonl` | `ae904b369008148763b2d9336b9110b4d7508efd7f95dc9a71f5167b7b4ddbdd` |
| Direct HTTP/1.1 | `/tmp/codex-newapi-loadgen.kr0a55/verified-origin-h1/do-origin-h1-c2400-20260807T0921Z/summary.json` | `19846e738e1bde72de922cb993f93f1b26a4741988aaad9a864663f06c5f9ab8` |
| Direct HTTP/1.1 | `/tmp/codex-newapi-loadgen.kr0a55/verified-origin-h1/do-origin-h1-c2400-20260807T0921Z/samples.jsonl` | `623acb4e2aadb2d17322f9ce018c930200489ded1ac87f5dd373edf2a81cf6f3` |
| Sonnet max-one calibration | `/tmp/codex-newapi-loadgen.kr0a55/verified-sonnet/do-sonnet-c300-20260807T0937Z/summary.json` | `e21c5c1a274a636414ec4d5abcdf7cd1d1e9df58497b645e25b85201f5320c47` |
| Sonnet first segment | `/tmp/codex-newapi-loadgen.kr0a55/verified-sonnet-retry/do-sonnet-c300-retry-20260807T0940Z/summary.json` | `b9f0d6dec3d02aeed090d281ee5912ce218ae38a9ca9ed9e93eb050e24f658eb` |
| Sonnet first segment | `/tmp/codex-newapi-loadgen.kr0a55/verified-sonnet-retry/do-sonnet-c300-retry-20260807T0940Z/samples.jsonl` | `b150be1f8bb8e15a5ae4e212c9b154409ebb1044a21487d86b7d684668f472a4` |
| Sonnet continuation | `/tmp/codex-newapi-loadgen.kr0a55/verified-sonnet-cont/do-sonnet-c300-cont-20260807T0945Z/summary.json` | `9be605a8b7ea64a8ec9841ef1dd4e43895355c6a837204a7ebd2c73ecce85c29` |
| Sonnet continuation | `/tmp/codex-newapi-loadgen.kr0a55/verified-sonnet-cont/do-sonnet-c300-cont-20260807T0945Z/samples.jsonl` | `fa7ee353ee7c75598c1d217c68d66767e79da7cbf8a90a5998f07626a9511353` |

Monitoring evidence remains under these local directories:

- `/tmp/newapi-do-ramp-monitor.izGtjI`
- `/tmp/newapi-do-origin-c2400.4Bc1pJ`
- `/tmp/newapi-do-origin-h1-c2400.DJb7iM`
- `/tmp/newapi-sonnet-c300.swvq2m`
- `/tmp/newapi-sonnet-c300-cont31.E8fali`

## Cleanup status

Cleanup completed and verified on 2026-08-07. The operator removed the copied
API credential and stress binaries, destroyed the temporary Droplet, deleted
the ephemeral DigitalOcean SSH key and tag, and removed local ephemeral SSH and
provisioning files. No snapshot was created. The downloaded and verified
benchmark artifacts listed above remain available under `/tmp`.
