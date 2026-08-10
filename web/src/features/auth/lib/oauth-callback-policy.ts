/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { SystemStatus } from '../types'

export type OAuthCallbackProvider =
  | 'github'
  | 'discord'
  | 'oidc'
  | 'linuxdo'
  | 'custom'

function parseOrigin(raw: unknown): URL | null {
  if (typeof raw !== 'string' || !raw.trim()) return null
  try {
    return new URL(raw)
  } catch {
    return null
  }
}

function isHTTPLoopback(origin: URL): boolean {
  if (origin.protocol !== 'http:') return false
  const hostname = origin.hostname.toLowerCase()
  return (
    hostname === 'localhost' ||
    hostname === '::1' ||
    hostname === '[::1]' ||
    hostname.startsWith('127.')
  )
}

export function isOAuthProviderAvailableAtOrigin(
  status: SystemStatus | null,
  provider: OAuthCallbackProvider,
  currentOrigin: string
): boolean {
  if (!status) return false
  const canonical = parseOrigin(
    status.oauth_canonical_origin ?? status.data?.oauth_canonical_origin
  )
  const current = parseOrigin(currentOrigin)
  if (!canonical || !current) return false
  if (canonical.origin === current.origin) return true

  if (isHTTPLoopback(canonical) && isHTTPLoopback(current)) {
    const secure = Boolean(
      status.session_cookie_secure ?? status.data?.session_cookie_secure
    )
    return !secure
  }

  if (canonical.protocol !== 'https:' || current.protocol !== 'https:') {
    return false
  }
  const trustedOrigins =
    status.oauth_trusted_origins ?? status.data?.oauth_trusted_origins ?? []
  if (!trustedOrigins.includes(current.origin)) return false
  const trustedAliasProviders =
    status.oauth_trusted_alias_providers ??
    status.data?.oauth_trusted_alias_providers ??
    []
  return provider !== 'custom' && trustedAliasProviders.includes(provider)
}
