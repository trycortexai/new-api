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

import { captureAffiliateCodeFromSearch } from '../storage'

describe('affiliate query capture', () => {
  test('persists a trimmed affiliate code before a route redirect', () => {
    const values = new Map<string, string>()

    captureAffiliateCodeFromSearch('?aff=%20invite-code%20', {
      getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value),
    })

    expect(values.get('aff')).toBe('invite-code')
  })

  test('does not replace a saved code when the query has no usable affiliate', () => {
    const values = new Map([['aff', 'saved-code']])

    captureAffiliateCodeFromSearch('?aff=%20%20', {
      getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value),
    })

    expect(values.get('aff')).toBe('saved-code')
  })
})
