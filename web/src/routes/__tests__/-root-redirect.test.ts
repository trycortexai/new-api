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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { resolveRootRedirect } from '../index'

describe('root route hostname redirects', () => {
  test('redirects a logged-in llmapi visitor to the dashboard overview', () => {
    assert.equal(
      resolveRootRedirect('llmapi.withcortex.ai', true),
      '/dashboard/overview'
    )
  })

  test('redirects a logged-out llmapi visitor to sign in', () => {
    assert.equal(resolveRootRedirect('llmapi.withcortex.ai', false), '/sign-in')
  })

  test('keeps the homepage for the canonical newapi hostname', () => {
    assert.equal(resolveRootRedirect('newapi.withcortex.ai', false), null)
  })
})
