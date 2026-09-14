import { describe, it, expect } from 'vitest'
import { versionsRows, unknownLine, installedRows } from '../src/versions.js'

// Ответ /routers/{id}/versions: снимок, новости и -- отдельно -- причины
// незнания. Живые числа сняты с рабочего роутера 12.09.2026.
const PAYLOAD = {
  rows: [
    {
      component: 'awgmgr',
      name: 'awg-manager',
      installed: '2.17.2',
      available: '2.18.0',
      hint: 'Обновление может сменить модуль ядра, и VPN-туннели поднимутся только после перезагрузки роутера.',
    },
    { component: 'firmware', name: 'KeeneticOS', installed: '5.02.A.8.0-3', available: '5.02.A.9.0-0' },
  ],
  unknown: [],
  installed: {
    awgmgr: '2.18.2+r2',
    hrneo: '3.18.3',
    hrneo_installed: true,
    firmware: '5.02.A.8.0-3',
    keenetic_os: 'KN-1811',
    kmod: '3.1.20260906',
    kmod_loaded: true,
  },
  checked_at: '2026-09-12T08:00:00Z',
}

describe('unknownLine', () => {
  // Каждая причина говорит словами. Раньше все четыре выглядели одинаково --
  // блока на экране просто не было, и выключенный источник читался как
  // «всё актуально».
  it('каждая причина «неизвестно» говорит словами', () => {
    expect(unknownLine('upstream_unavailable')).toContain('Проверить обновления не удалось')
    expect(unknownLine('upstream_not_configured')).toBe('Проверка обновлений не настроена — мы не знаем, что вышло.')
    expect(unknownLine('no_snapshot')).toBe('Роутер ещё не рассказал про версии.')
    expect(unknownLine('agent_too_old')).toBe('Агент на роутере старый и про модуль ядра не сообщает.')
  })

  // Обещание без метки времени говорит больше, чем мы знаем.
  it('к «не удалось» подставляет, когда смотрели в последний раз', () => {
    expect(unknownLine('upstream_unavailable', '3 часа назад')).toBe(
      'Проверить обновления не удалось, последний раз смотрели 3 часа назад.',
    )
  })

  // Список причин закрытый: незнакомую причину экран не показывает вовсе,
  // вместо того чтобы вывести человеку её код.
  it('незнакомую причину не выдумывает', () => {
    expect(unknownLine('something_new')).toBe('')
    expect(unknownLine(undefined)).toBe('')
  })
})

describe('versionsRows', () => {
  it('новость о панели называет последствие, а не «нажми и станет лучше»', () => {
    const row = versionsRows(PAYLOAD)[0]
    expect(row.text).toContain('может сменить модуль ядра')
    expect(row.text).toContain('VPN-туннели поднимутся только после перезагрузки роутера')
  })

  // Последствие обновления панели известно и без подсказки сервера: старый
  // бэкенд её не пришлёт, и молчать об уроненных туннелях экран не имеет права.
  it('без подсказки сервера последствие всё равно названо', () => {
    const row = versionsRows({ rows: [{ component: 'awgmgr', name: 'awg-manager', installed: '2.17.2', available: '2.18.0' }] })[0]
    expect(row.text).toContain('может сменить модуль ядра')
  })

  it('называет и что вышло, и что стоит на роутере', () => {
    const row = versionsRows(PAYLOAD)[0]
    expect(row.text).toContain('«2.18.0»')
    expect(row.text).toContain('«2.17.2»')
  })

  it('без новостей строк не выдумывает', () => {
    expect(versionsRows({ rows: [], unknown: [] })).toEqual([])
    expect(versionsRows(null)).toEqual([])
  })

  // Кнопки «обновить пакет» рядом с новостью нет: точечного обновления у
  // агента не существует, а кнопка без бэкенда не рисуется.
  it('не предлагает действий, которых нет за кнопкой', () => {
    for (const row of versionsRows(PAYLOAD)) {
      expect(row.action).toBeUndefined()
    }
  })
})

describe('installedRows', () => {
  it('показывает, что стоит на роутере', () => {
    const byKey = Object.fromEntries(installedRows(PAYLOAD).map((r) => [r.key, r]))
    expect(byKey.awgmgr.value).toBe('2.18.2+r2')
    expect(byKey.hrneo.value).toBe('3.18.3')
  })

  // «Про HydraRoute сведений нет» и «HydraRoute не установлен» -- разные
  // ответы. Опрос мог не дать ответа, и у владельца, у которого HydraRoute
  // стоит и работает, второе было бы прямым враньём.
  it('про HydraRoute без ответа говорит «сведений нет», а не «не установлен»', () => {
    const rows = installedRows({ installed: { awgmgr: '2.18.2+r2' } })
    const hrneo = rows.find((r) => r.key === 'hrneo')
    expect(hrneo.value).toBe('сведений нет')
    for (const row of rows) {
      expect(row.value).not.toContain('не установлен')
    }
  })

  it('без снимка строк не выдумывает', () => {
    expect(installedRows({})).toEqual([])
    expect(installedRows(null)).toEqual([])
  })

  // «Про загрузку модуля ядра ответа нет» и «модуль не загружен» -- разные
  // состояния, ровно как у HydraRoute. false означает поломку, и выдавать
  // молчание агента за неё нельзя: у владельца исправного роутера это была бы
  // выдуманная авария.
  it('про модуль ядра без ответа не пишет «не загружен»', () => {
    const rows = installedRows({ installed: { kmod: '3.1.20260906' } })
    const kmod = rows.find((r) => r.key === 'kmod')
    expect(kmod.value).toBe('3.1.20260906')
    for (const row of rows) {
      expect(row.valueSub ?? '').not.toContain('не загружен')
    }
  })

  it('а настоящее «не загружен» показывает', () => {
    const rows = installedRows({ installed: { kmod: '3.1.20260906', kmod_loaded: false } })
    const kmod = rows.find((r) => r.key === 'kmod')
    expect(kmod.valueSub).toContain('не загружен')
  })

  it('версии модуля ядра нет вовсе — «сведений нет», а не «не загружен»', () => {
    const rows = installedRows({ installed: { awgmgr: '2.18.2+r2' } })
    const kmod = rows.find((r) => r.key === 'kmod')
    expect(kmod.value).toBe('сведений нет')
    expect(kmod.valueSub ?? '').not.toContain('не загружен')
  })
})

// Всё, что читает владелец, -- по-русски, «VPN-туннель» полной формой.
// Имена программ (awg-manager, HydraRoute Neo, KeeneticOS, AmneziaWG) человек
// видит в панели своего роутера, поэтому они остаются латиницей и из проверки
// исключаются -- вместе с номерами версий.
describe('тон текстов', () => {
  const PRODUCT_NAMES = /awg-manager|HydraRoute Neo|HydraRoute|KeeneticOS|AmneziaWG|VPN-туннел[а-я]*/g

  // Проверяется ПРОЗА, а не данные. Номера версий и модели («5.02.A.8.0-3»,
  // «KN-1811») латиницу содержат по праву -- их печатает сам роутер, и человек
  // видит их в его панели. Поэтому значения в «ёлочках» из проверки
  // вырезаются, а остаётся то, что мы написали сами.
  function prose() {
    const out = []
    for (const row of versionsRows(PAYLOAD)) out.push(row.text, row.title)
    // Строка БЕЗ серверной подсказки: тогда последствие берётся из клиентской
    // константы, и проверка тона наконец касается и её тоже. На payload с hint
    // эта константа в проверяемые тексты не попадала вовсе, то есть половина
    // проверки была вакуумной.
    const noHint = versionsRows({
      rows: [{ component: 'awgmgr', name: 'awg-manager', installed: '2.17.2', available: '2.18.0' }],
    })
    for (const row of noHint) out.push(row.text, row.title)
    for (const row of installedRows(PAYLOAD)) out.push(row.title)
    for (const reason of ['upstream_unavailable', 'upstream_not_configured', 'no_snapshot', 'agent_too_old']) {
      out.push(unknownLine(reason))
    }
    return out.filter(Boolean)
  }

  it('в текстах нет английского', () => {
    for (const text of prose()) {
      const rest = text
        .replace(/«[^»]*»/g, '') // значения и имена -- данные, а не наша проза
        .replace(PRODUCT_NAMES, '')
        .replace(/[0-9.+\-—,.:()\s]/g, '')
      expect(rest).not.toMatch(/[a-zA-Z]/)
    }
  })

  it('в текстах нет «VPN» без «туннеля»', () => {
    for (const text of prose()) {
      const bare = text.replace(/VPN-туннел[а-я]*/g, '')
      expect(bare).not.toContain('VPN')
    }
  })
})
