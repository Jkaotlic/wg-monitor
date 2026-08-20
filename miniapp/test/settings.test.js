import { describe, it, expect } from 'vitest'
import { thresholdRows, auditRows, doctorRows, pingRows, firmwareStatus } from '../src/settings.js'

// Пороги живут в backend.yaml и больше нигде: экран печатает то, что прислал
// сервер (miniappSettingsResp), а не числа из макета.
describe('thresholdRows', () => {
  it('называет пороги словами последствия, значение — справа', () => {
    const rows = thresholdRows({
      silence_after_sec: 120,
      alert_after_fails: 3,
      recovery_after_oks: 2,
      agent_version: 'v0.16.0',
    })
    const byKey = Object.fromEntries(rows.map((r) => [r.key, r]))
    expect(byKey.silence.title).toContain('молчит')
    expect(byKey.silence.value).toBe('2 мин без отчёта')
    expect(byKey.alert.value).toBe('3 проверки подряд')
    expect(byKey.recovery.value).toBe('2 проверки подряд')
    expect(byKey.agent.value).toBe('v0.16.0')
  })

  // У мобильного роутера «молчит» и «выключен» -- разные события, и второе
  // число появляется только у него.
  it('мобильному добавляет порог «выключен»', () => {
    const rows = thresholdRows({ silence_after_sec: 1800, offline_after_sec: 86400, mobile: true })
    expect(rows.some((r) => r.key === 'offline')).toBe(true)
  })

  it('без ответа сервера строк не выдумывает', () => {
    expect(thresholdRows(null)).toEqual([])
  })
})

// version_audit отвечает JSON'ом (wire.VersionAudit) -- это строки данных, а
// не простыня.
describe('auditRows', () => {
  const AUDIT = JSON.stringify({
    awgmgr_version: '2.17.2',
    awgmgr_running: true,
    hrneo_installed: true,
    hrneo_running: true,
    hrneo_version: '2.4.0',
    firmware_current: '4.3.7',
    firmware_avail: '4.3.8',
  })

  it('версии становятся строками со значением', () => {
    const byKey = Object.fromEntries(auditRows(AUDIT).map((r) => [r.key, r]))
    expect(byKey.awgmgr.value).toBe('2.17.2')
    expect(byKey.hrneo.value).toBe('2.4.0')
    expect(byKey.firmware.value).toBe('4.3.7')
    // Доступное обновление -- это новость, а не примечание мелким шрифтом.
    expect(byKey.firmware.sub).toContain('4.3.8')
    expect(byKey.firmware.tone).toBe('warn')
  })

  it('остановленная служба видна тоном, а не только словом', () => {
    const rows = auditRows(JSON.stringify({ awgmgr_version: '2.17.2', awgmgr_running: false, firmware_current: '4.3.7' }))
    const awgm = rows.find((r) => r.key === 'awgmgr')
    expect(awgm.tone).toBe('danger')
    expect(awgm.sub).toContain('не работает')
  })

  it('неразобранный ответ не превращается в выдумку', () => {
    expect(auditRows('готово')).toEqual([])
  })
})

// router_doctor отвечает строками вида "✅ имя — подробность".
describe('doctorRows', () => {
  const OUT = [
    '🩺 Проверка роутера',
    '✅ awg-manager API — 2.17.2',
    '⚠️ ping-check — выключен на awg10',
    '❌ wg-monitor agent — процесс не найден',
  ].join('\n')

  it('строка доктора разбирается в название и значение', () => {
    const rows = doctorRows(OUT)
    expect(rows).toHaveLength(3)
    expect(rows[0]).toEqual({ key: 'd0', title: 'awg-manager API', value: '2.17.2', tone: 'ok' })
    expect(rows[1].tone).toBe('warn')
    expect(rows[2].tone).toBe('danger')
  })

  // Строка без подробности -- это «да» без измерения, и прочерк тут ставить
  // нечем: значение берётся из самого вердикта.
  it('строка без подробности несёт вердикт значением', () => {
    expect(doctorRows('✅ маршрут по умолчанию')[0].value).toBe('в порядке')
  })

  it('незнакомый вывод показывается как есть, а не теряется', () => {
    expect(doctorRows('что-то пошло не так')).toEqual([])
  })
})

// Проверка связи читается из проекции туннеля: ответ pingcheck_status несёт
// топологию роутера и мини-аппу не отдаётся.
describe('pingRows', () => {
  const TUNNELS = [
    { tunnel_id: 'awg12', name: 'Amsterdam', ping_check_status: 'ok', ping_latency_ms: 38 },
    { tunnel_id: 'awg10', name: 'Frankfurt', ping_check_status: 'disabled' },
    { tunnel_id: 'awg7', ping_check_status: 'unknown' },
  ]

  it('живая проверка показывает задержку', () => {
    const rows = pingRows(TUNNELS)
    expect(rows[0].value).toBe('38 мс')
    expect(rows[0].enabled).toBe(true)
  })

  it('выключенная проверка предлагает включить, а не молчит', () => {
    const row = pingRows(TUNNELS)[1]
    expect(row.enabled).toBe(false)
    expect(row.value).toBe('выключена')
  })

  it('неизвестное состояние остаётся неизвестным', () => {
    const row = pingRows(TUNNELS)[2]
    expect(row.enabled).toBe(null)
    expect(row.value).toBe('неизвестно')
  })
})

// firmware_status отвечает JSON'ом (wire.FirmwareStatus).
describe('firmwareRows', () => {
  it('доступное обновление названо и помечено', () => {
    const s = firmwareStatus(JSON.stringify({ current: '4.3.7', available: '4.3.8', channel: 'stable', hint: 'перезагрузка ~3 мин' }))
    expect(s.known).toBe(true)
    expect(s.updateAvailable).toBe(true)
    expect(s.rows.find((r) => r.key === 'current').value).toBe('4.3.7')
    expect(s.rows.find((r) => r.key === 'available').value).toBe('4.3.8')
    expect(s.rows.find((r) => r.key === 'available').tone).toBe('warn')
    // Подсказка роутера -- фраза, и строкой данных она не становится: в
    // правой колонке значение не переносится и налезло бы на заголовок.
    expect(s.rows.some((r) => r.key === 'hint')).toBe(false)
    expect(s.hint).toContain('перезагрузка')
  })

  // Свежая прошивка -- тоже ответ, и строка про неё не исчезает: пустое
  // место читается как «не проверяли».
  it('свежая прошивка говорит об этом прямо', () => {
    const s = firmwareStatus(JSON.stringify({ current: '4.3.8' }))
    expect(s.updateAvailable).toBe(false)
    expect(s.rows.find((r) => r.key === 'available').value).toBe('обновления нет')
  })

  it('чужой ответ не выдаётся за состояние прошивки', () => {
    expect(firmwareStatus('готово').known).toBe(false)
  })
})
