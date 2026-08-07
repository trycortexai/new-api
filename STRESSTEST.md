# Streaming stress-test runbook

This runbook covers safe use of `tools/newapi-stress` from a local machine or a
temporary DigitalOcean Droplet. It uses placeholders for endpoints and resource
identifiers. Never put an API key, DigitalOcean token, IP address, or private
key in this file, a command-line argument, a run artifact, or Git.

The runner uses closed-loop concurrency and does not retry requests. Every run
starts with one validated preflight request. Read
`tools/newapi-stress/README.md` before changing its limits or success rules.

## Prerequisites

- Go 1.22 or newer.
- `jq` for artifact inspection.
- `shasum` or `sha256sum` for artifact checksums.
- An API key available as `NEW_API_KEY` in the current shell. Enter it with a
  silent prompt or load it from a local ignored environment file. Do not echo
  it.
- For the DigitalOcean procedure: authenticated `doctl`, `ssh`, `scp`, and
  `ssh-keygen`.
- Explicit approval before any paid-model run.

Confirm local tools without printing credentials:

```bash
go version
jq --version
doctl version
test -n "${NEW_API_KEY:-}" && echo "API key is set"
doctl account get --format Email,Status
```

Do not enable shell tracing while a key is present. If tracing is already on,
disable it before continuing:

```bash
set +x
```

Run the deterministic tests before sending load:

```bash
go test ./tools/newapi-stress
```

## Local fake-model ramp

Create a dedicated temporary artifact directory. This keeps generated files out
of the worktree unless they are intentionally copied later.

```bash
LOCAL_RUN_ROOT="$(mktemp -d /tmp/newapi-stress.local.XXXXXX)"
chmod 700 "$LOCAL_RUN_ROOT"
```

First validate authentication, routing, HTTP/2, SSE framing, and fake-model
usage with one request:

```bash
go run ./tools/newapi-stress \
  --target 'https://<api-base-url>/v1' \
  --preflight-only \
  --output-dir "$LOCAL_RUN_ROOT"
```

Inspect the new summary before load. The preflight must report `success: true`
and the expected fake-model usage. Do not proceed after an authentication,
protocol, content, or usage failure.

```bash
find "$LOCAL_RUN_ROOT" -name summary.json -type f -print
jq '{run_id, target, model, http_version, preflight}' \
  "$LOCAL_RUN_ROOT"/*/summary.json
```

The original three-minute ramp starts at c300 and doubles until a stage is
below 90% success or reaches the hard ceiling:

```bash
go run ./tools/newapi-stress \
  --target 'https://<api-base-url>/v1' \
  --start-concurrency 300 \
  --max-concurrency 4800 \
  --stage-duration 3m \
  --success-threshold 0.90 \
  --http-version 2 \
  --output-dir "$LOCAL_RUN_ROOT"
```

For a shorter diagnostic ramp, make the shorter duration and ceiling explicit:

```bash
go run ./tools/newapi-stress \
  --target 'https://<api-base-url>/v1' \
  --start-concurrency 600 \
  --max-concurrency 2400 \
  --stage-duration 1m \
  --success-threshold 0.90 \
  --http-version 2 \
  --output-dir "$LOCAL_RUN_ROOT"
```

Do not interpret a local failure as service capacity until the same request has
been run from a controlled generator. Wi-Fi, VPN, NAT, endpoint security, and
HTTP/2 connection loss can terminate many streams together.

## Temporary DigitalOcean generator

The following commands assume zsh or Bash. They create a unique local working
directory, a one-use SSH key, a tag, and one Droplet. Keep the exact IDs emitted
by DigitalOcean; cleanup later uses those IDs rather than names, tags, or broad
filters.

### 1. Prepare local files

```bash
DO_RUN_TMP="$(mktemp -d /tmp/newapi-stress.do.XXXXXX)"
chmod 700 "$DO_RUN_TMP"

LOAD_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
LOAD_NAME="newapi-loadgen-${LOAD_STAMP}"
LOAD_TAG="newapi-loadgen-${LOAD_STAMP}"
SSH_KEY_NAME="newapi-loadgen-${LOAD_STAMP}"
SSH_PRIVATE_KEY="$DO_RUN_TMP/id_ed25519"
KNOWN_HOSTS_FILE="$DO_RUN_TMP/known_hosts"

ssh-keygen -q -t ed25519 -N '' -f "$SSH_PRIVATE_KEY"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -o "$DO_RUN_TMP/newapi-stress" ./tools/newapi-stress
```

### 2. Create the temporary resources

Create the tag and import only the generated public key:

```bash
doctl compute tag create "$LOAD_TAG"
SSH_KEY_ID="$(doctl compute ssh-key import "$SSH_KEY_NAME" \
  --public-key-file "$SSH_PRIVATE_KEY.pub" \
  --format ID --no-header)"
test -n "$SSH_KEY_ID"
```

Create one generator. Select a currently available region, image, and size for
the intended load. The example shape has four vCPUs and 8 GiB RAM. Do not reuse
a production Droplet.

```bash
DROPLET_ROW="$(doctl compute droplet create "$LOAD_NAME" \
  --region '<region-slug>' \
  --size 'c2-4vcpu-8gb' \
  --image 'ubuntu-24-04-x64' \
  --ssh-keys "$SSH_KEY_ID" \
  --tag-name "$LOAD_TAG" \
  --enable-monitoring \
  --wait \
  --format ID,PublicIPv4 \
  --no-header)"

IFS=$'\t ' read -r DROPLET_ID DROPLET_IP <<< "$DROPLET_ROW"
test -n "$DROPLET_ID"
test -n "$DROPLET_IP"
```

Resolve and review the exact resource before connecting. Do not paste this
output into tickets or repository files because it contains infrastructure
identifiers and an IP address.

```bash
doctl compute droplet get "$DROPLET_ID" \
  --format ID,Name,PublicIPv4,Memory,VCPUs,Region,Status,Tags
```

Record the host key in the dedicated file. Verify its fingerprint through the
DigitalOcean console or another trusted channel before accepting it.

```bash
ssh-keyscan -H "$DROPLET_IP" > "$KNOWN_HOSTS_FILE"
ssh-keygen -lf "$KNOWN_HOSTS_FILE"
```

Copy only the compiled runner:

```bash
scp -i "$SSH_PRIVATE_KEY" \
  -o IdentitiesOnly=yes \
  -o UserKnownHostsFile="$KNOWN_HOSTS_FILE" \
  -o StrictHostKeyChecking=yes \
  "$DO_RUN_TMP/newapi-stress" "root@${DROPLET_IP}:/root/newapi-stress"
```

### 3. Run the fake ramp

Choose a unique run ID. Send the API key through SSH standard input so it does
not appear in the remote command line or on remote disk. The remote runner does
not read standard input.

```bash
FAKE_RUN_ID="do-fake-ramp-${LOAD_STAMP}"

printf '%s\n' "$NEW_API_KEY" | ssh -i "$SSH_PRIVATE_KEY" \
  -o IdentitiesOnly=yes \
  -o UserKnownHostsFile="$KNOWN_HOSTS_FILE" \
  -o StrictHostKeyChecking=yes \
  "root@${DROPLET_IP}" \
  'IFS= read -r NEW_API_KEY && export NEW_API_KEY &&
   /root/newapi-stress \
     --target "https://<api-base-url>/v1" \
     --run-id '"$FAKE_RUN_ID"' \
     --start-concurrency 600 \
     --max-concurrency 4800 \
     --stage-duration 1m \
     --success-threshold 0.90 \
     --http-version 2 \
     --output-dir /root/stress-results'
```

If the SSH session is interrupted, reconnect and inspect the exact run
directory. Do not automatically restart a paid run or combine partial results
without recording the interruption.

## Explicitly gated paid Sonnet run

Paid mode accepts only `claude-sonnet-5` and requires all billing interlocks.
Before proceeding:

1. Get explicit approval for model, concurrency, duration, request cap, and
   maximum estimated list cost.
2. Verify the current model price and account billing multiplier.
3. Run a single preflight and confirm content, usage, and the observed billing
   delta.
4. Confirm the fake ramp has finished and no other generator process is active.

The approved c300, two-minute example has a $1 estimate cap and a 50,000-request
hard cap. The runner limits `max_tokens` to two and sends no retries.

Local example:

```bash
go run ./tools/newapi-stress \
  --target 'https://<api-base-url>/v1' \
  --model claude-sonnet-5 \
  --allow-paid-sonnet \
  --start-concurrency 300 \
  --max-concurrency 300 \
  --stage-duration 2m \
  --max-cost-usd 1 \
  --max-requests 50000 \
  --http-version 2 \
  --output-dir "$LOCAL_RUN_ROOT"
```

DigitalOcean example using the existing temporary generator:

```bash
SONNET_RUN_ID="do-sonnet-c300-${LOAD_STAMP}"

printf '%s\n' "$NEW_API_KEY" | ssh -i "$SSH_PRIVATE_KEY" \
  -o IdentitiesOnly=yes \
  -o UserKnownHostsFile="$KNOWN_HOSTS_FILE" \
  -o StrictHostKeyChecking=yes \
  "root@${DROPLET_IP}" \
  'IFS= read -r NEW_API_KEY && export NEW_API_KEY &&
   /root/newapi-stress \
     --target "https://<api-base-url>/v1" \
     --run-id '"$SONNET_RUN_ID"' \
     --model claude-sonnet-5 \
     --allow-paid-sonnet \
     --start-concurrency 300 \
     --max-concurrency 300 \
     --stage-duration 2m \
     --max-cost-usd 1 \
     --max-requests 50000 \
     --http-version 2 \
     --output-dir /root/stress-results'
```

The cost cap can overshoot by at most one already in-flight c300 wave. A failed
request can also be charged without returning usage. Treat the billing ledger,
not the local estimate, as the source of truth. If a response has unknown usage,
the runner stops scheduling; investigate before any continuation.

## Collect and verify artifacts

Choose a durable local result directory outside `DO_RUN_TMP`; cleanup deletes
`DO_RUN_TMP`. Copy each exact run directory before deleting the Droplet.

```bash
RESULT_ROOT="$(pwd)/stress-results/${LOAD_STAMP}"
mkdir -p "$RESULT_ROOT"

scp -r -i "$SSH_PRIVATE_KEY" \
  -o IdentitiesOnly=yes \
  -o UserKnownHostsFile="$KNOWN_HOSTS_FILE" \
  -o StrictHostKeyChecking=yes \
  "root@${DROPLET_IP}:/root/stress-results/${FAKE_RUN_ID}" \
  "$RESULT_ROOT/"
```

Repeat the copy for `SONNET_RUN_ID` only if that paid run was approved and run.
Then verify the summaries and create a checksum manifest:

```bash
jq '{run_id, target, model, started_at, finished_at, preflight, stages,
     total_usage, stop_reason}' \
  "$RESULT_ROOT"/*/summary.json

find "$RESULT_ROOT" -type f -print0 | sort -z | \
  xargs -0 shasum -a 256 > "$RESULT_ROOT/SHA256SUMS"
```

Keep `summary.json`, `samples.jsonl`, the checksum manifest, generator metrics,
and correlated service metrics or sanitized logs. Never collect shell history,
environment dumps, SSH private keys, API keys, DigitalOcean tokens, raw app
configuration, or files containing credentials. This repository does not
ignore `stress-results/`; review `git status` and do not stage raw artifacts by
accident.

## Exact-ID cleanup

Cleanup is mandatory even after a failed or interrupted run. First try to
confirm that the artifacts are local and readable. Missing artifacts are a data
loss warning, not a reason to leave paid infrastructure running. Then resolve
the exact Droplet ID and compare its name and tag with the values created in
this shell:

```bash
if ! test -f "$RESULT_ROOT/$FAKE_RUN_ID/summary.json"; then
  echo "Warning: local summary is missing; continue cleanup after recording this" >&2
fi

doctl compute droplet get "$DROPLET_ID" \
  --format ID,Name,PublicIPv4,Status,Tags
printf 'expected name: %s\nexpected tag: %s\n' "$LOAD_NAME" "$LOAD_TAG"
```

Delete only that exact numeric Droplet ID. Do not use `--tag-name` for cleanup,
because a tag-wide delete can affect more than the intended resource.

```bash
doctl compute droplet delete "$DROPLET_ID" --force

if doctl compute droplet get "$DROPLET_ID" >/dev/null 2>&1; then
  echo "Droplet still exists; stop cleanup and investigate" >&2
  return 1 2>/dev/null || exit 1
fi
```

Resolve and delete only the imported SSH key ID:

```bash
doctl compute ssh-key get "$SSH_KEY_ID" --format ID,Name,FingerPrint
doctl compute ssh-key delete "$SSH_KEY_ID" --force

if doctl compute ssh-key get "$SSH_KEY_ID" >/dev/null 2>&1; then
  echo "SSH key still exists; stop cleanup and investigate" >&2
  return 1 2>/dev/null || exit 1
fi
```

Check the exact temporary tag and delete that tag by name after the Droplet is
gone:

```bash
doctl compute tag get "$LOAD_TAG" --format Name,DropletCount
doctl compute tag delete "$LOAD_TAG" --force

if doctl compute tag get "$LOAD_TAG" >/dev/null 2>&1; then
  echo "Tag still exists; stop cleanup and investigate" >&2
  return 1 2>/dev/null || exit 1
fi
```

Finally remove the local one-use binary, private key, public key, and host-key
file. The path guard refuses to recurse outside the `mktemp` pattern. It does
not remove `RESULT_ROOT`.

```bash
case "$DO_RUN_TMP" in
  /tmp/newapi-stress.do.*)
    test -d "$DO_RUN_TMP" && test ! -L "$DO_RUN_TMP" &&
      rm -rf -- "$DO_RUN_TMP"
    ;;
  *)
    echo "Refusing to remove unexpected path: $DO_RUN_TMP" >&2
    return 1 2>/dev/null || exit 1
    ;;
esac

unset NEW_API_KEY DROPLET_ID DROPLET_IP SSH_KEY_ID
```

Record cleanup as complete only after all three DigitalOcean `get` checks fail
and the guarded local temporary directory no longer exists.
