# Cortex dual-region deployment

This deployment mirrors the existing Cortex API/Edge split without replacing
`api.withcortex.ai` or `apicn.withcortex.ai`.

| Site                | Public hostname          | Runtime                                         | Database and cache                                                                              |
| ------------------- | ------------------------ | ----------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| International       | `newapi.withcortex.ai`   | DigitalOcean App Platform, NYC                  | `new_api` database on `cortex-prod` PostgreSQL 16 and the existing `redis-prod` Redis 7 cluster |
| International alias | `llmapi.withcortex.ai`   | Alias of the same DigitalOcean App Platform app | Uses the same international PostgreSQL and Redis data plane                                     |
| Hong Kong route     | `newapicn.withcortex.ai` | Existing Hong Kong server, Caddy reverse proxy  | Uses the international site's PostgreSQL and Redis through `newapi.withcortex.ai`               |

There is one application deployment and one authoritative data plane. The Hong
Kong endpoint is a stateless acceleration route to the international endpoint;
it does not run New API, PostgreSQL, or Redis locally.

## Before deploying

1. Create the `new_api` database and `new_api` database user on the
   `cortex-prod` DigitalOcean PostgreSQL cluster. Make `new_api` the owner of
   the database and its `public` schema so startup migrations can create and
   alter tables.
2. Generate a `SESSION_SECRET` with at least 64 random characters for the
   international deployment.
3. Confirm that the GitHub App connected to DigitalOcean can read the private
   `trycortexai/new-api` repository.
4. Authorize the App Platform app in both the `cortex-prod` PostgreSQL and
   `redis-prod` trusted-source firewalls.
5. Confirm that the existing HK Caddy host can reach
   `https://newapi.withcortex.ai/api/status`.
6. Do not attach this deployment or route to `api.withcortex.ai` or
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
doctl apps update <app-id> --spec <temporary-spec-with-existing-secret>
```

The checked-in spec intentionally does not contain `SESSION_SECRET`. Before
updating an existing app, merge its current encrypted `SESSION_SECRET` entry
into an untracked temporary copy of the spec and apply that copy. Never write
the generated secret or its encrypted value to the repository. After the
update, confirm that `SESSION_SECRET` is still present with type `SECRET`
before sending traffic.

## Hong Kong site

Add the `deploy/cortex/hk/Caddyfile` site block to the existing HK Caddy
configuration. It terminates TLS for `newapicn.withcortex.ai`, preserves the
client-facing forwarded host, and sends the origin request to
`https://newapi.withcortex.ai` with the correct TLS server name and Host header.

```bash
caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Point the DNS record for `newapicn.withcortex.ai` at the existing HK server
only after the route is healthy. No application secrets, database credentials,
or persistent volumes are needed on the HK host.

## Verification

The deployment-specific public branding is declared in
`deploy/cortex/branding-options.json`. Apply those values through the root-only
`PUT /api/option/` endpoint after the initial administrator setup.

The homepage body intentionally contains only the Cortex service description.
The required New API and QuantumNous attribution remains in the site footer.
Docs and About are disabled in `HeaderNavModules`; the Cortex product and
documentation destination is `https://withcortex.ai/`.

The canonical `newapi.withcortex.ai` hostname keeps this homepage. The
`llmapi.withcortex.ai` alias does not render a homepage at `/`: after the
existing authentication bootstrap completes, authenticated visitors are
redirected to `/dashboard/overview` and unauthenticated visitors are redirected
to `/sign-in`.

OAuth callback validation accepts only the canonical origin and the exact
origins configured in `SESSION_COOKIE_TRUSTED_URL`. Provider registration
depends on the provider's callback model:

| Provider     | Registration requirement                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| GitHub       | Register only `https://newapi.withcortex.ai/oauth/github`. A GitHub OAuth App has one callback setting, but GitHub's documented matching accepts `https://llmapi.withcortex.ai/oauth/github` and `https://newapicn.withcortex.ai/oauth/github` because the host excluding subdomains, port, and callback path match. See [GitHub's redirect URL rules](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#redirect-urls). |
| Discord      | Register all three exact callbacks: `https://newapi.withcortex.ai/oauth/discord`, `https://llmapi.withcortex.ai/oauth/discord`, and `https://newapicn.withcortex.ai/oauth/discord`. [Discord applications expose a `redirect_uris` list](https://docs.discord.com/developers/resources/application#application-object-application-structure).                                                                                                                |
| OIDC         | Register all three exact callbacks: `https://newapi.withcortex.ai/oauth/oidc`, `https://llmapi.withcortex.ai/oauth/oidc`, and `https://newapicn.withcortex.ai/oauth/oidc`. [OIDC clients define a `redirect_uris` array](https://openid.net/specs/openid-connect-registration-1_0.html#ClientMetadata); every used value must be registered exactly.                                                                                                         |
| LinuxDO      | Register only `https://newapi.withcortex.ai/oauth/linuxdo`. The gateway does not offer LinuxDO login or binding on trusted aliases because this deployment has one client configuration and no checked multi-callback capability.                                                                                                                                                                                                                            |
| Custom OAuth | Replace `<slug>` in `https://newapi.withcortex.ai/oauth/<slug>` and register that canonical callback. Custom providers are not offered on trusted aliases because each provider has one client configuration and no per-origin capability setting.                                                                                                                                                                                                           |

Keep the three origins in `.do/new-api-intl.yaml`'s
`SESSION_COOKIE_TRUSTED_URL` value synchronized with the callback origins each
provider supports.
Authorization and token exchange use the initiating origin bound to the
one-time OAuth state; exact-origin validation must not be replaced with
wildcards.

The same callback policy is enforced by `POST /api/oauth/state` and by the
sign-in and account-binding interfaces: GitHub, Discord, and OIDC are visible
on the two trusted aliases; LinuxDO and custom OAuth providers are visible only
on the canonical hostname. In insecure loopback development, every provider
remains available on the exact browser origin accepted by the local callback
gate.

The logo URL uses the canonical `newapi.withcortex.ai` hostname. While DNS is
pending, production may temporarily use the DigitalOcean ingress hostname for
the same `/cortex-logo.svg` asset.

Run the same checks against both public hostnames:

```bash
curl -fsS https://newapi.withcortex.ai/api/status
curl -fsS https://llmapi.withcortex.ai/api/status
curl -fsS https://newapicn.withcortex.ai/api/status
```

Both responses must include `"success":true` and report the same New API
version and setup state. Complete the setup wizard once through the
international endpoint, then create a low-quota test token and verify the same
token through both routes:

```bash
curl -fsS https://newapi.withcortex.ai/v1/models \
  -H "Authorization: Bearer <international-test-token>"

curl -fsS https://llmapi.withcortex.ai/v1/models \
  -H "Authorization: Bearer <international-test-token>"

curl -fsS https://llmapi.withcortex.ai/v1/chat/completions \
  -H "Authorization: Bearer <international-test-token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"<low-cost-model>","messages":[{"role":"user","content":"Reply with exactly OK"}],"stream":false,"max_tokens":1}'

curl -fsS https://newapicn.withcortex.ai/v1/models \
  -H "Authorization: Bearer <hong-kong-test-token>"
```

Do not promote the HK DNS route or add real upstream credentials until status,
login, token creation, model listing, and one streaming completion pass through
both hostnames.
