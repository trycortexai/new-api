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
// @ts-expect-error Bun's test runtime is available in CI but is not part of the
// production TypeScript project references.
import { describe, expect, test } from 'bun:test'

import type { SystemStatus } from '../../types'
import {
  isOAuthProviderAvailableAtOrigin,
  type OAuthCallbackProvider,
} from '../oauth-callback-policy'

const providers: OAuthCallbackProvider[] = [
  'github',
  'discord',
  'oidc',
  'linuxdo',
  'custom',
]

describe('OAuth callback provider visibility', () => {
  test('offers every configured provider on the canonical origin', () => {
    const status: SystemStatus = {
      oauth_canonical_origin: 'https://newapi.withcortex.ai',
      session_cookie_secure: true,
      oauth_trusted_alias_providers: ['github', 'discord', 'oidc'],
      oauth_trusted_origins: [
        'https://llmapi.withcortex.ai',
        'https://newapicn.withcortex.ai',
      ],
    }

    for (const provider of providers) {
      expect(
        isOAuthProviderAvailableAtOrigin(
          status,
          provider,
          'https://newapi.withcortex.ai'
        )
      ).toBe(true)
    }
  })

  test('offers only providers with checked alias callback support on HTTPS aliases', () => {
    const status: SystemStatus = {
      oauth_canonical_origin: 'https://newapi.withcortex.ai',
      session_cookie_secure: true,
      oauth_trusted_alias_providers: ['github', 'discord', 'oidc'],
      oauth_trusted_origins: [
        'https://llmapi.withcortex.ai',
        'https://newapicn.withcortex.ai',
      ],
    }

    for (const provider of ['github', 'discord', 'oidc'] as const) {
      expect(
        isOAuthProviderAvailableAtOrigin(
          status,
          provider,
          'https://llmapi.withcortex.ai'
        )
      ).toBe(true)
    }
    for (const provider of ['linuxdo', 'custom'] as const) {
      expect(
        isOAuthProviderAvailableAtOrigin(
          status,
          provider,
          'https://llmapi.withcortex.ai'
        )
      ).toBe(false)
    }
  })

  test('keeps every provider on exact insecure loopback development origins', () => {
    const status: SystemStatus = {
      oauth_canonical_origin: 'http://localhost:3000',
      session_cookie_secure: false,
    }

    for (const provider of providers) {
      expect(
        isOAuthProviderAvailableAtOrigin(
          status,
          provider,
          'http://localhost:5173'
        )
      ).toBe(true)
    }
  })

  test('does not treat secure or non-loopback HTTP origins as trusted aliases', () => {
    const secureLocal: SystemStatus = {
      oauth_canonical_origin: 'http://localhost:3000',
      session_cookie_secure: true,
    }
    const remoteHTTP: SystemStatus = {
      oauth_canonical_origin: 'http://newapi.example.test',
      session_cookie_secure: false,
    }

    expect(
      isOAuthProviderAvailableAtOrigin(
        secureLocal,
        'github',
        'http://localhost:5173'
      )
    ).toBe(false)
    expect(
      isOAuthProviderAvailableAtOrigin(
        remoteHTTP,
        'github',
        'http://alias.example.test'
      )
    ).toBe(false)
  })

  test('fails closed on an alias when the backend advertises no provider capability', () => {
    const status: SystemStatus = {
      oauth_canonical_origin: 'https://newapi.withcortex.ai',
      session_cookie_secure: true,
    }

    expect(
      isOAuthProviderAvailableAtOrigin(
        status,
        'github',
        'https://llmapi.withcortex.ai'
      )
    ).toBe(false)
  })

  test('hides alias-capable providers on unlisted HTTPS origins', () => {
    const status: SystemStatus = {
      oauth_canonical_origin: 'https://newapi.withcortex.ai',
      session_cookie_secure: true,
      oauth_trusted_alias_providers: ['github', 'discord', 'oidc'],
      oauth_trusted_origins: ['https://llmapi.withcortex.ai'],
    }

    expect(
      isOAuthProviderAvailableAtOrigin(
        status,
        'github',
        'https://evil.withcortex.ai'
      )
    ).toBe(false)
  })
})
