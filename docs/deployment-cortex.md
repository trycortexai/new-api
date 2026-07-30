# Cortex dual-region deployment

This deployment mirrors the existing Cortex API/Edge split without replacing
`api.withcortex.ai` or `apicn.withcortex.ai`.

| Site | Public hostname | Runtime | Database and cache |
| --- | --- | --- | --- |
| International | `newapi.withcortex.ai` | DigitalOcean App Platform, NYC | `new_api` database on `cortex-prod` PostgreSQL 16 and the existing `redis-prod` Redis 7 cluster |
| Hong Kong | `newapicn.withcortex.ai` | Existing Hong Kong server, Docker Compose behind Caddy | Dedicated PostgreSQL 16 and Redis 7 volumes on the HK server |

The two sites are intentionally isolated. They do not replicate users,
channels, quotas, tokens, or logs between regions.

## Before deploying

1. Create the `new_api` database and `new_api` database user on the
   `cortex-prod` DigitalOcean PostgreSQL cluster.
2. Generate an independent `SESSION_SECRET` for each region with at least
   64 random characters.
3. Confirm that the GitHub App connected to DigitalOcean can read the private
   `trycortexai/new-api` repository.
4. Make the `ghcr.io/trycortexai/new-api` package readable by the Hong Kong
   server, or configure a read-only GHCR token there.
5. Do not attach either deployment to `api.withcortex.ai` or
   `apicn.withcortex.ai`; those hostnames belong to the existing Cortex API.

## International site

The reviewed App Platform spec is `.do/new-api-intl.yaml`. It follows the
existing Cortex production shape: GitHub-driven Dockerfile builds, NYC App
Platform runtime, PostgreSQL 16, Redis 7, health checks, deployment/domain
alerts, and automatic deploys from `main`.

Set `SESSION_SECRET` as an encrypted runtime environment variable on the
`new-api-intl` service before sending production traffic. Keep the initial
instance count at one until that secret is configured. If the existing
database cluster uses different database or user names, update only
`db_name` and `db_user` before applying the spec.

After DigitalOcean access is refreshed:

```bash
doctl apps create --spec .do/new-api-intl.yaml
```

If the app already exists, update it using its resolved app ID:

```bash
doctl apps update <app-id> --spec .do/new-api-intl.yaml
```

## Hong Kong site

Copy `deploy/cortex/hk` to the existing HK server. Create `.env` from
`.env.example`, replace every placeholder, authenticate Docker to GHCR if the
package is private, and start the stack:

```bash
cd deploy/cortex/hk
cp .env.example .env
docker compose config
docker compose pull
docker compose up -d
```

Point the Cloudflare DNS record for `newapicn.withcortex.ai` at the existing HK
server only after the stack is healthy. Caddy obtains and renews the public TLS
certificate and proxies requests to New API over its private Docker network.
PostgreSQL and Redis are not published on host ports.

## Verification

Run the same checks against both public hostnames:

```bash
curl -fsS https://newapi.withcortex.ai/api/status
curl -fsS https://newapicn.withcortex.ai/api/status
```

Both responses must include `"success":true`. Complete the setup wizard
separately in each region, then create a low-quota test token and verify:

```bash
curl -fsS https://newapi.withcortex.ai/v1/models \
  -H "Authorization: Bearer <international-test-token>"

curl -fsS https://newapicn.withcortex.ai/v1/models \
  -H "Authorization: Bearer <hong-kong-test-token>"
```

Do not promote DNS or add real upstream credentials until status, login, token
creation, model listing, and one streaming completion pass in each region.
