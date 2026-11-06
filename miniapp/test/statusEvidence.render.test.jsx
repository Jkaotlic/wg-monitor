// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { render } from 'preact'
import { EvidenceSummary } from '../src/components/EvidenceSummary.jsx'

it('показывает общий вывод и раскрываемые показания каждого источника', () => {
  const root = document.createElement('div')
  render(<EvidenceSummary evidence={{
    state: 'degraded', reason_code: 'source_conflict', affected_routes: 18,
    sources: [
      { source: 'bot_check', state: 'ok', observed_at: '2026-11-06T10:00:00Z' },
      { source: 'awg_manager', state: 'disconnected', observed_at: '2026-11-06T10:00:00Z' },
    ],
  }} />, root)

  expect(root.querySelector('summary').textContent).toContain('Источники расходятся')
  expect(root.querySelector('details').open).toBe(false)
  expect([...root.querySelectorAll('li')].map((x) => x.textContent)).toEqual([
    'Проверка бота: отвечает',
    'Панель VPN: нет связи',
  ])
})
