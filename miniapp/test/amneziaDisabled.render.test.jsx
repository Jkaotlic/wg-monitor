// @vitest-environment jsdom
// v0.61.3: кабинет Amnezia Premium отключён. Сервер отвечает кодом
// amnezia_disabled и готовой фразой; экран показывает её как есть -- в выпуске,
// отзыве, добавлении ключа, файле в личку, мастере замены и автопочинке.
// Карточка подписки без стран кнопок выпуска не рисует.
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { ApiError } from '../src/api.js'
import { cabinetErrorText, cabinetPerms, issueFailure, revokeErrorText, sendConfErrorText } from '../src/cabinetKeys.js'
import { startErrorText } from '../src/replace.js'
import { errorText } from '../src/errorText.js'
import { accountSummary } from '../src/cabinet.js'
import { CabinetOptions } from '../src/screens/CabinetOptions.jsx'

const TEXT =
  'Выпуск через кабинет Amnezia Premium отключён: автор awg-manager предупреждает, что получение конфигов через кабинет может заблокировать подписку. Конфиги берите в приложении или личном кабинете Amnezia.'
const off = () => new ApiError(410, 'amnezia_disabled', '/routers/4/vpn/issue failed: 410', TEXT)

describe('amnezia_disabled -- фраза сервера', () => {
  it('выпуск', () => {
    expect(issueFailure(off(), cabinetPerms('owner'), 'amnezia')).toEqual({ text: TEXT, offerRevoke: false })
  })
  it('добавление ключа, отзыв страны, файл в личку', () => {
    expect(cabinetErrorText('amnezia', off())).toBe(TEXT)
    expect(revokeErrorText(off())).toBe(TEXT)
    expect(sendConfErrorText(off())).toBe(TEXT)
  })
  it('мастер замены', () => {
    expect(startErrorText(off())).toBe(TEXT)
  })
  it('включение автопочинки', () => {
    expect(errorText(off())).toBe(TEXT)
  })
})

describe('карточка Amnezia без стран', () => {
  const account = { provider: 'amnezia', label: 'Amnezia Premium', connected: false, note: TEXT }

  it('выпуск закрыт, причина -- фраза сервера', () => {
    const s = accountSummary(account, { canRevoke: true })
    expect(s.canIssue).toBe(false)
    expect(s.reason).toBe(TEXT)
  })

  it('ни одной кнопки выпуска или отзыва', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () =>
      render(
        <CabinetOptions routerID={4} routerName="home" kind="amnezia" account={account} perms={cabinetPerms('owner')} openSheet={() => {}} onPick={() => {}} onChanged={() => {}} />,
        root,
      ),
    )
    expect(root.querySelectorAll('button').length).toBe(0)
    expect(root.textContent).toContain(TEXT)
    render(null, root)
    root.remove()
  })
})
