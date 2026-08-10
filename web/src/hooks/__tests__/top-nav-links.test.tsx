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
import { afterAll, afterEach, describe, test } from 'bun:test'
import assert from 'node:assert/strict'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobalKeys = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'SVGElement',
  'Node',
  'Element',
] as const
const originalDomGlobals = new Map(
  domGlobalKeys.map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
)

for (const key of domGlobalKeys) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { ROLE } = await import('@/lib/roles')
const { useAuthStore } = await import('@/stores/auth-store')
const { useTopNavLinks } = await import('../use-top-nav-links')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        Console: 'Console',
        Rankings: 'Rankings',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
const originalReactActEnvironment = reactTestGlobals.IS_REACT_ACT_ENVIRONMENT
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function TopNavLinksHarness() {
  const links = useTopNavLinks()

  return (
    <nav>
      {links.map((link) => (
        <a key={link.href} href={link.href}>
          {link.title}
        </a>
      ))}
    </nav>
  )
}

async function renderTopNav(role?: number) {
  useAuthStore
    .getState()
    .auth.setUser(
      role === undefined ? null : { id: 1, username: 'nav-user', role }
    )

  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const queryClient = new QueryClient()
  queryClient.setQueryData(['status'], {
    HeaderNavModules: JSON.stringify({
      home: true,
      console: true,
      pricing: { enabled: false, requireAuth: false },
      rankings: { enabled: true, requireAuth: false },
      docs: false,
      about: false,
    }),
  })
  await act(async () =>
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <TopNavLinksHarness />
        </I18nextProvider>
      </QueryClientProvider>
    )
  )

  return { container, queryClient, root }
}

describe('top navigation links', () => {
  afterEach(() => {
    useAuthStore.getState().auth.reset()
    document.body.replaceChildren()
  })

  afterAll(() => {
    domWindow.close()
    for (const [key, descriptor] of originalDomGlobals) {
      if (descriptor) {
        Object.defineProperty(globalThis, key, descriptor)
      } else {
        Reflect.deleteProperty(globalThis, key)
      }
    }
    if (originalReactActEnvironment === undefined) {
      Reflect.deleteProperty(reactTestGlobals, 'IS_REACT_ACT_ENVIRONMENT')
    } else {
      reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = originalReactActEnvironment
    }
  })

  test('shows Console without the duplicate Home link', async () => {
    const rendered = await renderTopNav(ROLE.USER)

    assert.equal(rendered.container.querySelector('a[href="/"]'), null)
    assert.equal(
      rendered.container.querySelector('a[href="/dashboard"]')?.textContent,
      'Console'
    )

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  for (const [name, role] of [
    ['signed-out visitors', undefined],
    ['users', ROLE.USER],
    ['admins', ROLE.ADMIN],
  ] as const) {
    test(`hides Rankings from ${name}`, async () => {
      const rendered = await renderTopNav(role)

      assert.equal(
        rendered.container.querySelector('a[href="/rankings"]'),
        null
      )

      await act(async () => rendered.root.unmount())
      rendered.queryClient.clear()
    })
  }

  test('shows Rankings to Root users', async () => {
    const rendered = await renderTopNav(ROLE.SUPER_ADMIN)

    assert.equal(
      rendered.container.querySelector('a[href="/rankings"]')?.textContent,
      'Rankings'
    )

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })
})
