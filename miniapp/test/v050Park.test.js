import { describe, it, expect } from 'vitest'
import { fleetRow, fleetSummary, fleetSummaryLine, stateCountLabel, routerSwitchChoices } from '../src/fleet.js'
import { parkRank, fleetRouterRows, parkCardLine, parkCardTone, warningFoldTitle } from '../src/fleetAdmin.js'
import { navReducer } from '../src/nav.js'

const r = (over) => ({ id: 1, nickname: 'x', status: 'online', last_seen_age_sec: 30, ...over })

describe('один словарь состояний (спека п. 2.3)', () => {
  it('подписи с согласованием', () => {
    expect(stateCountLabel('ok', 5)).toBe('в порядке')
    expect(stateCountLabel('attention', 1)).toBe('тревога')
    expect(stateCountLabel('attention', 3)).toBe('тревоги')
    expect(stateCountLabel('silent', 1)).toBe('молчит')
    expect(stateCountLabel('silent', 2)).toBe('молчат')
    expect(stateCountLabel('silent', 0)).toBe('молчат')
  })

  it('пилюля выключенного -- «молчит», как в сводке', () => {
    expect(fleetRow(r({ status: 'offline', last_seen_age_sec: 7200 })).pill).toEqual({ tone: 'danger', text: 'молчит 2 ч' })
  })

  it('строка сводки: все в порядке / смесь', () => {
    expect(fleetSummaryLine(fleetSummary([r({ id: 1 }), r({ id: 2 })]))).toBe('Все 2 в порядке.')
    expect(fleetSummaryLine(fleetSummary([r({ id: 1 })]))).toBe('Роутер в порядке.')
    const mixed = fleetSummary([
      r({ id: 1, status: 'alert' }),
      r({ id: 2, status: 'offline', last_seen_age_sec: 9000 }),
      r({ id: 3, status: 'offline', last_seen_age_sec: 9000 }),
      r({ id: 4 }),
    ])
    expect(fleetSummaryLine(mixed)).toBe('4 роутера: 1 тревога, 2 молчат, 1 в порядке.')
    expect(fleetSummaryLine(fleetSummary([]))).toBe('Роутеров пока нет.')
  })
})

describe('Парк: порядок карточек (спека п. 2.4)', () => {
  it('тревога → молчит → агент отстаёт → остальные', () => {
    expect(parkRank(r({ status: 'alert' }))).toBe(0)
    expect(parkRank(r({ status: 'offline', last_seen_age_sec: 9000 }))).toBe(1)
    expect(parkRank(r({ last_seen_age_sec: null }))).toBe(1)
    expect(parkRank(r({ agent_behind: true }))).toBe(2)
    expect(parkRank(r({}))).toBe(3)
    const rows = fleetRouterRows({
      routers: [
        r({ id: 1, nickname: 'а-ок' }),
        r({ id: 2, nickname: 'б-отстаёт', agent_behind: true }),
        r({ id: 3, nickname: 'в-молчит', status: 'offline', last_seen_age_sec: 9000 }),
        r({ id: 4, nickname: 'г-тревога', status: 'alert' }),
      ],
    })
    expect(rows.map((x) => x.id)).toEqual([4, 3, 2, 1])
  })

  it('пилюля строки -- из списка роутеров, если он есть (та же, что в «Мои роутеры»)', () => {
    const fleet = { routers: [r({ id: 7, status: 'alert' })] }
    const list = [r({ id: 7, status: 'alert', reserve_only_alert: true })]
    expect(fleetRouterRows(fleet, list)[0].pill.text).toBe('резерв не работает')
    expect(fleetRouterRows(fleet)[0].pill.text).toBe('тревога')
    expect(fleetRouterRows(fleet)[0].state).toBeUndefined()
  })

  it('одна строка причины/версии', () => {
    expect(parkCardLine({ sub: 'отчёт 1 мин назад', update: { text: '' }, router: { agent_version: 'v0.47.0' } })).toBe('отчёт 1 мин назад · агент v0.47.0')
    expect(parkCardLine({ sub: 'не на связи 4 дн', update: { text: 'ждёт включения: v0.49.0 поставится, когда роутер выйдет на связь' }, router: { agent_version: 'v0.30.0' } })).toBe(
      'не на связи 4 дн · ждёт включения: v0.49.0 поставится, когда роутер выйдет на связь',
    )
  })

  it('оживление в пути выигрывает у версии в той же строке; тон сохраняется', () => {
    const row = { sub: 'не на связи 4 дн', update: { text: '', tone: 'ok' }, router: { agent_version: 'v0.30.0' } }
    expect(parkCardLine(row, { text: 'ждём выхода на связь', tone: 'sig' })).toBe('не на связи 4 дн · оживление: ждём выхода на связь')
    expect(parkCardTone(row, { text: 'ждём выхода на связь', tone: 'sig' })).toBe('sig')
    expect(parkCardTone({ ...row, update: { text: 'не ставится v0.49.0: x', tone: 'danger' } }, { text: '' })).toBe('danger')
    expect(parkCardTone({ ...row, update: { text: 'агент отстаёт от бэкенда', tone: 'warn' } })).toBe('warn')
    expect(parkCardTone(row)).toBe('')
    expect(parkCardTone({ ...row, update: { text: 'ставится v0.49.0', tone: 'muted' } })).toBe('')
  })

  it('оговорки одной свёрнутой строкой', () => {
    expect(warningFoldTitle(1)).toBe('Что может помешать обновлению · 1')
    expect(warningFoldTitle(3)).toBe('Что может помешать обновлению · 3')
    expect(warningFoldTitle(5)).toBe('Что может помешать обновлению · 5')
  })
})

describe('переключатель роутера (спека п. 2.6)', () => {
  const many = (n) => Array.from({ length: n }, (_, i) => r({ id: i + 1, nickname: `r${i + 1}` }))

  it('варианты с пилюлей и отметкой текущего; поиск только при > 6', () => {
    const five = routerSwitchChoices(many(5), 2)
    expect(five.search).toBe(false)
    expect(five.choices).toHaveLength(5)
    expect(five.choices.find((c) => c.value === 2).current).toBe(true)
    expect(five.choices[0].pill).toEqual({ tone: 'ok', text: 'в порядке' })
    expect(routerSwitchChoices(many(7), 1).search).toBe(true)
    expect(routerSwitchChoices(many(12), 1).search).toBe(true)
  })

  it('смена роутера сохраняет вкладку роутера', () => {
    const s = { routerID: 1, tab: 'diag', overlay: null, sheet: { title: 'x' } }
    expect(navReducer(s, { type: 'router', id: 2, keepTab: true })).toMatchObject({ routerID: 2, tab: 'diag', sheet: null })
    expect(navReducer(s, { type: 'router', id: 2 })).toMatchObject({ routerID: 2, tab: 'router' })
    expect(navReducer({ ...s, tab: 'park' }, { type: 'router', id: 2, keepTab: true }).tab).toBe('router')
  })

  it('переход «router → tunnels» одним действием', () => {
    const s = { routerID: null, tab: 'park', overlay: 'awg3panel', overlayParams: { panelId: 'main' }, sheet: null }
    const next = navReducer(s, { type: 'router', id: 9, tab: 'tunnels' })
    expect(next).toMatchObject({ routerID: 9, tab: 'tunnels', overlay: null })
    expect('overlayParams' in next).toBe(false)
  })
})
