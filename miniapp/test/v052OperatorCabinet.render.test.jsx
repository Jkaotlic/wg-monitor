// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { CabinetOptions } from '../src/screens/CabinetOptions.jsx'
import { CabinetIssue } from '../src/screens/CabinetIssue.jsx'
import { cabinetPerms } from '../src/cabinetKeys.js'
import { replaceCanRevoke } from '../src/screens/ReplaceScreen.jsx'

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  return root
}
const texts = (root) => [...root.querySelectorAll('button')].map((b) => b.textContent.trim())

describe('v0.52 §7: оператор отзывает и присылает .conf', () => {
  const perms = cabinetPerms('operator')
  it('«Отозвать» у выпущенной страны', async () => {
    const account = { provider: 'amnezia', connected: true, options: [{ id: 'fi', label: 'Финляндия', issued: true }] }
    const root = await mount(<CabinetOptions routerID={4} routerName="home" kind="amnezia" account={account} perms={perms} openSheet={() => {}} onPick={() => {}} onChanged={() => {}} />)
    expect(texts(root)).toContain('Отозвать')
    render(null, root)
    root.remove()
  })
  it('«Прислать .conf в личку» на экране выпуска', async () => {
    const pending = { provider: 'amnezia', title: 'Amnezia', option: { id: 'nl', label: 'Нидерланды', note: '' } }
    const root = await mount(<CabinetIssue routerID={4} asleep={false} pending={pending} perms={perms} openSheet={() => {}} />)
    expect(texts(root)).toContain('Прислать .conf в личку')
    render(null, root)
    root.remove()
  })
  it('мастер замены: оператору доступен «отозвать старый»', async () => {
    expect(replaceCanRevoke('operator')).toBe(true)
    expect(replaceCanRevoke('owner')).toBe(true)
    expect(replaceCanRevoke('admin')).toBe(true)
    expect(replaceCanRevoke('')).toBe(false)
  })
})
