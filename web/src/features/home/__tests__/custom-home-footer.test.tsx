// @ts-expect-error Bun's test runtime is available in CI but is not part of the
// production TypeScript project references.
import { afterAll as after, describe, mock, test } from 'bun:test'
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

import { Window } from 'happy-dom'
import React from 'react'

const domWindow = new Window()
for (const key of [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

mock.module('@/components/layout', () => ({
  PublicLayout: ({ children }: { children: React.ReactNode }) => (
    <div data-public-layout>{children}</div>
  ),
}))
mock.module('@/components/layout/components/footer', () => ({
  Footer: () => <footer data-site-footer>Footer attribution</footer>,
}))
mock.module('@/components/rich-content', () => ({
  RichContent: ({ content }: { content: string }) => (
    <article data-home-content>{content}</article>
  ),
}))
mock.module('@/context/theme-provider', () => ({
  useTheme: () => ({ resolvedTheme: 'dark' }),
}))
mock.module('@/stores/auth-store', () => ({
  useAuthStore: () => ({ auth: { user: null } }),
}))
mock.module('react-i18next', () => ({
  useTranslation: () => ({
    i18n: { language: 'en' },
    t: (key: string) => key,
  }),
}))
mock.module('../components', () => ({
  CTA: () => null,
  Features: () => null,
  Hero: () => null,
  HowItWorks: () => null,
  Stats: () => null,
}))
mock.module('../hooks', () => ({
  useHomePageContent: () => ({
    content: 'Cortex API gateway',
    isLoaded: true,
    isUrl: false,
  }),
}))

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { Home } = await import('../index')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('custom Markdown homepage', () => {
  after(() => {
    domWindow.close()
  })

  test('places the configured footer after the main content', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => root.render(<Home />))

    const main = container.querySelector('main')
    const footer = container.querySelector('[data-site-footer]')
    assert.ok(main)
    assert.ok(footer)
    assert.equal(
      main.compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING,
      Node.DOCUMENT_POSITION_FOLLOWING
    )

    await act(async () => root.unmount())
    container.remove()
  })
})
