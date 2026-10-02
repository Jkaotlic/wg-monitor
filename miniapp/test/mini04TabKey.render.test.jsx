// @vitest-environment jsdom
// MINI-04: смена роутера пересоздаёт вкладку -- состояние A не переезжает на B.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { useState } from 'preact/hooks'

const seen = vi.hoisted(() => ({ mounts: 0 }))

function Stateful({ id, routerID }) {
  const [first] = useState(() => {
    seen.mounts++
    return id ?? routerID
  })
  return <div class="probe">{String(first)}</div>
}
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: Stateful }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: Stateful }))
vi.mock('../src/screens/ChecksTab.jsx', () => ({ ChecksTab: Stateful }))
vi.mock('../src/screens/ManageTab.jsx', () => ({ ManageTab: Stateful }))

const { TabBody } = await import('../src/screens/TabBody.jsx')

const ROUTERS = [
  { id: 1, nickname: 'lesnaya', status: 'online' },
  { id: 2, nickname: 'polevaya', status: 'online' },
]

describe('MINI-04: вкладка пересоздаётся при смене роутера', () => {
  for (const tab of ['router', 'tunnels', 'diag', 'manage']) {
    it(tab, async () => {
      const root = document.createElement('div')
      await act(async () => render(<TabBody nav={{ routerID: 1, tab }} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />, root))
      await act(async () => render(<TabBody nav={{ routerID: 2, tab }} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />, root))
      expect(root.querySelector('.probe').textContent).toBe('2')
      render(null, root)
    })
  }
})
