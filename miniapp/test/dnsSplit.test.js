import { describe, it, expect } from 'vitest'
import { dnsSplitView } from '../src/dnsSplit.js'

// Раздельный DNS: кому роутер отдал русские зоны, каким способом и как идут
// запросы к Яндексу. Проверка dns_split читает НАСТРОЙКИ роутера, а не меряет
// трафик: сравнение ответов резолверов ничего не различает (одинаковые адреса
// на все крупные русские сайты, прогон 14.09.2026). Экран говорит «по
// настройкам», и ни слова уверенности.

const check = (details) => ({ check_name: 'dns_split', status: 'ok', ts: '2026-09-14T10:00:00Z', details })
const allText = (v) =>
  [...v.rows.map((r) => r.text), v.route?.text, v.resolves?.text, v.note, ...(v.foot ?? [])].filter(Boolean).join('\n')

describe('dnsSplitView', () => {
  it('старый агент: проверка появится после обновления, а не «нет данных»', () => {
    const v = dnsSplitView([{ check_name: 'dns', status: 'ok' }])
    expect(v.missing).toBe(true)
    expect(v.note).toBe('Эта проверка появится после обновления агента на роутере.')
    expect(v.rows).toEqual([])
  })

  it('зоны группируются по вердикту, одна строка на группу, имена зон по-человечески', () => {
    const v = dnsSplitView([
      check({
        zones: { ru: 'yandex_dot', 'xn--p1ai': 'yandex_dot', su: 'yandex_dot', tatar: 'none' },
        resolves: 'ok',
        route: 'direct',
      }),
    ])
    expect(v.rows).toHaveLength(2)
    const ok = v.rows.find((r) => r.tone === 'ok')
    expect(ok.text).toBe('Яндекс, защищённое соединение: .ru, .рф, .su')
    const none = v.rows.find((r) => r.key === 'none')
    expect(none.text).toBe('Без отдельного правила — уходят на общие DNS-серверы: .tatar')
    expect(none.tone).toBe('warn')
  })

  it('поломка схемы идёт первой: человек видит её раньше нормы', () => {
    const v = dnsSplitView([check({ zones: { ru: 'other', su: 'yandex_dot', tatar: 'mixed' }, route: 'direct' })])
    expect(v.rows.map((r) => r.key)).toEqual(['other', 'mixed', 'yandex_dot'])
    expect(v.rows[0].text).toBe('Отданы другому DNS-серверу, не Яндексу: .ru')
    expect(v.rows[1].text).toBe('Поделены между Яндексом и другим DNS-сервером — часть запросов уйдёт не к Яндексу: .tatar')
  })

  it('Яндекс не тем способом и нечитаемые настройки -- разные слова', () => {
    const v = dnsSplitView([check({ zones: { ru: 'yandex_doh', su: 'unknown' }, route: 'unknown' })])
    const byKey = Object.fromEntries(v.rows.map((r) => [r.key, r]))
    expect(byKey.yandex_doh.text).toBe('Яндекс, но не тем способом, что в эталоне: .ru')
    expect(byKey.unknown.text).toBe('Неизвестно — роутер не отдал свои настройки: .su')
    expect(byKey.unknown.tone).toBe('muted')
  })

  it('маршрут напрямую -- «по правилам роутера», без обещаний про банк', () => {
    const v = dnsSplitView([check({ zones: { ru: 'yandex_dot' }, route: 'direct' })])
    expect(v.route).toEqual({ text: 'Запросы к Яндексу, по правилам роутера, идут напрямую, мимо VPN-туннеля.', tone: 'ok' })
  })

  it('маршрут через туннель называет туннель и последствие', () => {
    const v = dnsSplitView([check({ zones: { ru: 'yandex_dot' }, route: 'tunnel', route_tunnel: 'vpn-nl' })])
    expect(v.route.text).toBe(
      'Запросы к Яндексу идут через VPN-туннель «vpn-nl» — Яндекс видит заграничный адрес и может отдавать адреса сайтов не для России.',
    )
    expect(v.route.tone).toBe('warn')
  })

  it('маршрут неизвестен -- так и сказано', () => {
    const v = dnsSplitView([check({ zones: {}, route: 'unknown' })])
    expect(v.route).toEqual({ text: 'Не удалось узнать, как идут запросы к Яндексу.', tone: 'muted' })
  })

  it('проба живости: молчит, когда всё хорошо, и говорит, когда роутер не ответил', () => {
    expect(dnsSplitView([check({ zones: {}, route: 'direct', resolves: 'ok' })]).resolves).toBeNull()
    const bad = dnsSplitView([check({ zones: {}, route: 'direct', resolves: 'fail' })]).resolves
    expect(bad).toEqual({ text: 'Роутер не ответил на запрос имени сайта — сайты по имени могут не открываться.', tone: 'warn' })
  })

  it('оговорка: вывод по настройкам, а адрес для банка решает маршрут до банка', () => {
    const v = dnsSplitView([check({ zones: { ru: 'yandex_dot' }, route: 'direct' })])
    expect(v.foot).toEqual([
      'Это вывод по настройкам роутера, а не замер трафика.',
      'Какой адрес видит сам банк, решает маршрут до банка — проверьте его в «Куда пойдёт сайт» на экране маршрутов.',
    ])
  })

  it('молчащий роутер: всё выше -- на момент последнего отчёта', () => {
    const v = dnsSplitView([check({ zones: { ru: 'yandex_dot' }, route: 'direct' })], { silent: true })
    expect(v.foot[0]).toBe('Пока роутер молчит, это данные на момент последнего отчёта, а не на сейчас.')
  })

  it('ни слова уверенности и ни одного жаргона', () => {
    const v = dnsSplitView([
      check({
        zones: { ru: 'yandex_dot', su: 'yandex_doh', tatar: 'other', 'xn--p1acf': 'mixed', 'xn--d1acj3b': 'none', 'xn--80adxhks': 'unknown' },
        route: 'tunnel',
        route_tunnel: 'vpn-nl',
        resolves: 'fail',
      }),
    ])
    const text = allText(v)
    for (const w of ['точно', 'гарант', 'доказан', 'DoH', 'DoT', 'апстрим', 'URL', 'резолв', 'dns_split', 'xn--']) {
      expect(text, w).not.toContain(w)
    }
  })
})
