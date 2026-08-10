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

import { executeCreateOAuthFlow } from '../api'

describe('OAuth flow API contract', () => {
  test('sends the current origin and returns the server-authorized callback', async () => {
    let requestedPayload: unknown
    let skipAuthRefresh: boolean | undefined

    const flow = await executeCreateOAuthFlow(
      {
        getAffiliateCode: () => 'invite-code',
        getRedirectOrigin: () => 'https://llmapi.withcortex.ai',
        request: async (payload, options) => {
          requestedPayload = payload
          skipAuthRefresh = options.skipAuthRefresh
          return {
            success: true,
            message: '',
            data: {
              flow_token: 'state-token',
              redirect_uri: 'https://llmapi.withcortex.ai/oauth/test-provider',
            },
          }
        },
      },
      'test-provider',
      'login'
    )

    expect(requestedPayload).toEqual({
      provider: 'test-provider',
      intent: 'login',
      aff: 'invite-code',
      redirect_origin: 'https://llmapi.withcortex.ai',
    })
    expect(skipAuthRefresh).toBeTrue()
    expect(flow).toEqual({
      state: 'state-token',
      redirectUri: 'https://llmapi.withcortex.ai/oauth/test-provider',
    })
  })
})
