// @ts-expect-error Bun's test runtime is available in CI but is not part of the
// production TypeScript project references.
import { describe, expect, test } from 'bun:test'

import { shouldShowPasskeyCard } from '../lib/passkey-visibility'

describe('profile passkey visibility', () => {
  test('shows passkey management when the host supports passkeys', () => {
    expect(shouldShowPasskeyCard({ passkey_login: true })).toBe(true)
  })

  test('hides passkey management when ModelVisa disables passkeys', () => {
    expect(shouldShowPasskeyCard({ passkey_login: false })).toBe(false)
  })

  test('hides passkey management until capability status is loaded', () => {
    expect(shouldShowPasskeyCard(undefined)).toBe(false)
  })
})
