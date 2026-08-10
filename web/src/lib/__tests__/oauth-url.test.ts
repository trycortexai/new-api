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

import {
  buildCustomOAuthUrl,
  buildDiscordOAuthUrl,
  buildGitHubOAuthUrl,
  buildLinuxDOOAuthUrl,
  buildOIDCOAuthUrl,
} from '../oauth'

const callbackURI = 'https://llmapi.withcortex.ai/oauth/test-provider'

function expectOAuthParameters(
  rawURL: string,
  clientID: string,
  state: string
): URL {
  const url = new URL(rawURL)
  expect(url.searchParams.get('client_id')).toBe(clientID)
  expect(url.searchParams.get('state')).toBe(state)
  expect(url.searchParams.get('redirect_uri')).toBe(callbackURI)
  return url
}

describe('OAuth authorization URLs', () => {
  test('uses the state-bound callback for GitHub', () => {
    const url = expectOAuthParameters(
      buildGitHubOAuthUrl('github-client', 'github-state', callbackURI),
      'github-client',
      'github-state'
    )
    expect(url.origin + url.pathname).toBe(
      'https://github.com/login/oauth/authorize'
    )
  })

  test('uses the state-bound callback for Discord', () => {
    const url = expectOAuthParameters(
      buildDiscordOAuthUrl('discord-client', 'discord-state', callbackURI),
      'discord-client',
      'discord-state'
    )
    expect(url.origin + url.pathname).toBe(
      'https://discord.com/oauth2/authorize'
    )
  })

  test('uses the state-bound callback for OIDC', () => {
    const url = expectOAuthParameters(
      buildOIDCOAuthUrl(
        'https://identity.example.com/authorize',
        'oidc-client',
        'oidc-state',
        callbackURI
      ),
      'oidc-client',
      'oidc-state'
    )
    expect(url.origin + url.pathname).toBe(
      'https://identity.example.com/authorize'
    )
  })

  test('uses the state-bound callback for LinuxDO', () => {
    const url = expectOAuthParameters(
      buildLinuxDOOAuthUrl('linuxdo-client', 'linuxdo-state', callbackURI),
      'linuxdo-client',
      'linuxdo-state'
    )
    expect(url.origin + url.pathname).toBe(
      'https://connect.linux.do/oauth2/authorize'
    )
  })

  test('uses the state-bound callback for a custom provider', () => {
    const url = expectOAuthParameters(
      buildCustomOAuthUrl(
        'https://custom.example.com/authorize',
        'custom-client',
        'custom-state',
        callbackURI,
        'openid profile'
      ),
      'custom-client',
      'custom-state'
    )
    expect(url.searchParams.get('scope')).toBe('openid profile')
  })
})
