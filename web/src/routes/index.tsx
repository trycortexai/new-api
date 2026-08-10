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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { Home } from '@/features/home'
import { useAuthStore } from '@/stores/auth-store'

const LLM_API_HOSTNAME = 'llmapi.withcortex.ai'

export function resolveRootRedirect(
  hostname: string,
  isAuthenticated: boolean
): '/dashboard/overview' | '/sign-in' | null {
  if (hostname !== LLM_API_HOSTNAME) return null
  return isAuthenticated ? '/dashboard/overview' : '/sign-in'
}

export const Route = createFileRoute('/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    const target = resolveRootRedirect(
      window.location.hostname,
      Boolean(auth.user && auth.accessToken)
    )

    if (target === '/dashboard/overview') {
      throw redirect({
        to: '/dashboard/$section',
        params: { section: 'overview' },
        replace: true,
      })
    }
    if (target === '/sign-in') {
      throw redirect({ to: '/sign-in', replace: true })
    }
  },
  component: Home,
})
