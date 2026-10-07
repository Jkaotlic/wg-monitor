import { describe, it, expect } from 'vitest'
import {
  PORTHOP_MIN_VERSION,
  porthopAvailable,
  parsePorthopStatus,
  porthopFailure,
  watchedNames,
  porthopStatusRows,
  porthopButtons,
  porthopArgs,
  porthopOutcomeText,
  porthopDeadlineMs,
  porthopLegacyText,
} from '../src/porthop.js'
import { AGENT_OLDER_THAN_APP } from '../src/labels.js'

const BASE = {
  installed: true,
  running: true,
  auto: true,
  watched: ['opkgtun10', 'opkgtun12'],
  legacy: { found: false, running: false },
  hops_24h: 3,
  recovered_24h: 2,
  failed_24h: 1,
  last_event: '2026-10-07 12:00:00 opkgtun10: порт 30000 -> 41234, поток ожил (хендшейк 3 с)',
  script_path: '/opt/etc/wg-monitor/awg-porthop.sh',
  conf_path: '/opt/etc/wg-monitor/porthop.conf',
  log_path: '/opt/var/log/wg-monitor/porthop.log',
}
const ok = (v) => ({ status: 'ok', output: JSON.stringify(v) })

describe('porthop: кому экран', () => {
  it('только админ и агент от v0.57.0; пустая и предрелизная версия запрещают', () => {
    expect(PORTHOP_MIN_VERSION).toBe('v0.57.0')
    expect(porthopAvailable({ role: 'admin', agent_version: 'v0.57.0' })).toBe(true)
    expect(porthopAvailable({ role: 'admin', agent_version: 'v0.58.1' })).toBe(true)
    expect(porthopAvailable({ role: 'admin', agent_version: 'v0.56.9' })).toBe(false)
    expect(porthopAvailable({ role: 'admin', agent_version: 'v0.57.0-rc1' })).toBe(false)
    expect(porthopAvailable({ role: 'admin', agent_version: '' })).toBe(false)
    expect(porthopAvailable({ role: 'owner', agent_version: 'v0.57.0' })).toBe(false)
    expect(porthopAvailable(null)).toBe(false)
  })
})

describe('parsePorthopStatus', () => {
  it('разбирает ответ агента в форму экрана', () => {
    const s = parsePorthopStatus(ok(BASE))
    expect(s).toMatchObject({
      installed: true,
      running: true,
      auto: true,
      ifaces: [],
      watched: ['opkgtun10', 'opkgtun12'],
      legacy: { found: false, running: false, path: '', movedTo: '' },
      hops: 3,
      recovered: 2,
      failed: 1,
      logTail: '',
      logPath: '/opt/var/log/wg-monitor/porthop.log',
    })
    expect(s.lastEvent).toContain('поток ожил')
  })
  it('ручная копия и куда она перенесена', () => {
    const s = parsePorthopStatus(ok({ ...BASE, legacy: { found: false, path: '/opt/etc/init.d/S99awg-porthop', running: false, moved_to: '/opt/etc/wg-monitor/legacy/S99awg-porthop' } }))
    expect(s.legacy.movedTo).toBe('/opt/etc/wg-monitor/legacy/S99awg-porthop')
  })
  it('не JSON, не ok и без installed -- null', () => {
    expect(parsePorthopStatus({ status: 'ok', output: 'нечто' })).toBeNull()
    expect(parsePorthopStatus({ status: 'err', output: JSON.stringify(BASE) })).toBeNull()
    expect(parsePorthopStatus(ok({ running: true }))).toBeNull()
    expect(parsePorthopStatus(null)).toBeNull()
  })
  it('мусорные поля не роняют разбор', () => {
    const s = parsePorthopStatus(ok({ installed: false, running: 'да', watched: 'opkgtun10', ifaces: [1, 'opkgtun9'], hops_24h: 'много' }))
    expect(s).toMatchObject({ installed: false, running: false, watched: [], ifaces: ['opkgtun9'], hops: 0 })
  })
})

describe('porthopFailure', () => {
  it('legacy_running -- своя ветка с путём ручной копии', () => {
    const r = { status: 'err', output: 'legacy_running: на роутере есть ручная копия смены порта (/opt/etc/init.d/S99awg-porthop) — две копии дрались бы' }
    expect(porthopFailure(r)).toEqual({ kind: 'legacy', path: '/opt/etc/init.d/S99awg-porthop' })
  })
  it('старый агент, таймаут, занято, прочая ошибка', () => {
    expect(porthopFailure({ status: 'err', output: 'unknown action: porthop_status' }).kind).toBe('old')
    expect(porthopFailure({ status: 'timeout', output: '' }).kind).toBe('timeout')
    expect(porthopFailure({ status: 'locked', output: '' }).kind).toBe('busy')
    expect(porthopFailure({ status: 'err', output: 'start porthop: exit 1' }).kind).toBe('err')
    expect(porthopFailure(ok(BASE))).toBeNull()
    expect(porthopFailure(null)).toBeNull()
  })
})

describe('watchedNames', () => {
  it('имя VPN-туннеля по интерфейсу из снимка, регистр не мешает', () => {
    const snap = { tunnels: [{ id: 'awg10', name: 'Нидерланды', iface: 'OpkgTun10' }, { id: 'awg12', name: '', iface: 'opkgtun12' }] }
    expect(watchedNames(['opkgtun10', 'opkgtun12', 'opkgtun14'], snap)).toEqual(['Нидерланды', 'awg12', 'opkgtun14'])
  })
  it('без снимка -- имена интерфейсов как есть', () => {
    expect(watchedNames(['opkgtun10'], null)).toEqual(['opkgtun10'])
  })
})

describe('porthopStatusRows', () => {
  const rows = (s, snap) => Object.fromEntries(porthopStatusRows(parsePorthopStatus(ok(s)), snap).map((r) => [r.key, r]))
  it('включено и работает: что сторожит и счёт за сутки', () => {
    const r = rows(BASE, { tunnels: [{ id: 'awg10', name: 'Нидерланды', iface: 'opkgtun10' }] })
    expect(r.state.value).toBe('включено, работает')
    expect(r.state.tone ?? '').toBe('')
    expect(r.watched.value).toBe('Нидерланды, opkgtun12')
    expect(r.day.value).toBe('3 смены порта: ожили 2, не ожили 1')
    expect(r.last.value).toContain('поток ожил')
  })
  it('включено, но не работает -- предупреждение', () => {
    expect(rows({ ...BASE, running: false }).state).toMatchObject({ value: 'включено, но не работает', tone: 'danger' })
  })
  it('выключено -- что сторожило бы', () => {
    const r = rows({ ...BASE, installed: false, running: false, hops_24h: 0, recovered_24h: 0, failed_24h: 0, last_event: '' })
    expect(r.state).toMatchObject({ value: 'выключено', tone: 'warn' })
    expect(r.watched.title).toBe('Будет сторожить')
    expect(r.day).toBeUndefined()
    expect(r.last).toBeUndefined()
  })
  it('сторожить нечего -- честно', () => {
    expect(rows({ ...BASE, watched: [] }).watched).toMatchObject({ value: 'нет VPN-туннеля, который несёт весь трафик', tone: 'warn' })
  })
  it('явный список интерфейсов вместо авто -- подписан', () => {
    expect(rows({ ...BASE, auto: false, ifaces: ['opkgtun10'], watched: ['opkgtun10'] }).watched.value).toBe('opkgtun10 (заданы вручную)')
  })
  it('сутки без смен', () => {
    expect(rows({ ...BASE, hops_24h: 0, recovered_24h: 0, failed_24h: 0 }).day.value).toBe('смен порта не было')
  })
  it('одна смена -- склонение', () => {
    expect(rows({ ...BASE, hops_24h: 1, recovered_24h: 1, failed_24h: 0 }).day.value).toBe('1 смена порта: ожили 1, не ожили 0')
  })
  it('ручная копия в работе -- строка с предупреждением', () => {
    expect(rows({ ...BASE, installed: false, legacy: { found: true, path: '/opt/etc/init.d/S99awg-porthop', running: true } }).legacy).toMatchObject({ tone: 'warn' })
  })
})

describe('porthopButtons', () => {
  const b = (s, fail = null) => porthopButtons(s ? parsePorthopStatus(ok(s)) : null, fail)
  it('выключено -- «Включить», без «Выключить»', () => {
    expect(b({ ...BASE, installed: false, running: false })).toEqual({ install: 'Включить', remove: false, replace: false })
  })
  it('включено -- «Выключить»; не работает -- «Включить снова»', () => {
    expect(b(BASE)).toEqual({ install: '', remove: true, replace: false })
    expect(b({ ...BASE, running: false })).toEqual({ install: 'Включить снова', remove: true, replace: false })
  })
  it('ручная копия -- «Заменить ручную копию» вместо «Включить»', () => {
    expect(b({ ...BASE, installed: false, running: false, legacy: { found: true, path: '/x', running: true } })).toEqual({ install: '', remove: false, replace: true })
    expect(b({ ...BASE, installed: false, running: false }, { kind: 'legacy', path: '/x' })).toEqual({ install: '', remove: false, replace: true })
  })
  it('состояние неизвестно -- только «Проверить» и «Журнал» (ничего не включаем вслепую)', () => {
    expect(b(null)).toEqual({ install: '', remove: false, replace: false })
  })
})

describe('porthopArgs / сроки', () => {
  it('установка -- без списка (авто), замена -- с replace_legacy; журнал -- строки', () => {
    expect(porthopArgs('install')).toEqual({})
    expect(porthopArgs('replace')).toEqual({ replace_legacy: true })
    expect(porthopArgs('logs')).toEqual({ lines: 100 })
    expect(porthopArgs('status')).toEqual({})
    expect(porthopArgs('remove')).toEqual({})
  })
  it('установка дольше, спящему -- ещё пять минут', () => {
    expect(porthopDeadlineMs('install', false)).toBe(150_000)
    expect(porthopDeadlineMs('status', false)).toBe(90_000)
    expect(porthopDeadlineMs('remove', true)).toBe(150_000 + 5 * 60_000)
  })
})

describe('porthopOutcomeText', () => {
  it('итоги действий словами', () => {
    expect(porthopOutcomeText('install', ok(BASE))).toBe('Смена порта включена.')
    expect(porthopOutcomeText('install', ok({ ...BASE, running: false }))).toBe('Смена порта поставлена, но не запустилась — посмотрите журнал.')
    expect(porthopOutcomeText('remove', ok({ ...BASE, installed: false, running: false }))).toBe('Смена порта выключена.')
    expect(porthopOutcomeText('status', ok(BASE))).toBe('')
  })
  it('после замены ручной копии -- куда она перенесена и как вернуть', () => {
    const moved = ok({ ...BASE, legacy: { found: false, path: '/opt/etc/init.d/S99awg-porthop', running: false, moved_to: '/opt/etc/wg-monitor/legacy/S99awg-porthop' } })
    const text = porthopOutcomeText('replace', moved)
    expect(text).toContain('Смена порта включена.')
    expect(text).toContain('Ручная копия перенесена в /opt/etc/wg-monitor/legacy/S99awg-porthop')
    expect(text).toContain('вернуть — перенести файл обратно в /opt/etc/init.d')
  })
  it('отказы: ручная копия, старый агент, таймаут, ошибка', () => {
    expect(porthopOutcomeText('install', { status: 'err', output: 'legacy_running: копия (/opt/etc/init.d/S99awg-porthop)' })).toBe(porthopLegacyText('/opt/etc/init.d/S99awg-porthop'))
    expect(porthopOutcomeText('status', { status: 'err', output: 'unknown action: porthop_status' })).toBe(AGENT_OLDER_THAN_APP)
    expect(porthopOutcomeText('status', { status: 'timeout' })).toBe('Роутер не ответил вовремя — проверьте состояние ещё раз.')
    expect(porthopOutcomeText('install', { status: 'err', output: 'start porthop: exit 1' })).toBe('Роутер ответил ошибкой — подробности ниже.')
    expect(porthopOutcomeText('status', { status: 'ok', output: 'x' })).toBe('Роутер ответил непонятно — проверьте состояние ещё раз.')
  })
  it('текст про ручную копию -- без жаргона, с выходом', () => {
    const t = porthopLegacyText('/opt/etc/init.d/S99awg-porthop')
    expect(t).toContain('/opt/etc/init.d/S99awg-porthop')
    expect(t).toContain('Ничего не изменено')
    expect(t).toContain('«Заменить ручную копию»')
  })
})

describe('lastEventText', () => {
  it('метка со сдвигом пояса и без него -- одинаково, без «+0300»', async () => {
    const { lastEventText } = await import('../src/porthop.js')
    expect(lastEventText('2026-10-07 12:00:00 +0300 opkgtun10: порт 30000 -> 41234, поток ожил (хендшейк 3 с)')).toBe(
      '2026-10-07 12:00:00 opkgtun10: порт 30000 -> 41234, поток ожил (хендшейк 3 с)',
    )
    expect(lastEventText('2026-10-07 12:00:00 opkgtun10: порт 30000 -> 41234, поток не ожил')).toBe('2026-10-07 12:00:00 opkgtun10: порт 30000 -> 41234, поток не ожил')
    expect(lastEventText('2026-10-07 12:00:00 -0130 opkgtun10: listen-port 41234 не сработал (rc=1), пира возвращаю')).toBe(
      '2026-10-07 12:00:00 opkgtun10: listen-port 41234 не сработал (rc=1), пира возвращаю',
    )
    expect(lastEventText('')).toBe('')
  })
  it('строка «Последнее событие» -- без сдвига пояса', () => {
    const rows = porthopStatusRows(parsePorthopStatus(ok({ ...BASE, last_event: '2026-10-07 12:00:00 +0300 opkgtun10: поток ожил' })))
    expect(rows.find((r) => r.key === 'last').value).toBe('2026-10-07 12:00:00 opkgtun10: поток ожил')
  })
})

describe('porthopLegacyFoundText', () => {
  it('работающая и спящая ручная копия -- разными словами, с выходом', async () => {
    const { porthopLegacyFoundText } = await import('../src/porthop.js')
    expect(porthopLegacyFoundText({ path: '/opt/etc/init.d/S99awg-porthop', running: true })).toContain('уже работает ручная копия смены порта (/opt/etc/init.d/S99awg-porthop)')
    expect(porthopLegacyFoundText({ path: '', running: false })).toContain('запустится при перезагрузке')
    expect(porthopLegacyFoundText({ running: true })).toContain('«Заменить ручную копию»')
  })
})
