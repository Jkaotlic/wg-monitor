import { describe, it, expect } from 'vitest'
import { awgmBadge } from '../src/awgmBadge.js'

// Правило значка карточки VPN-туннеля в UI awg-manager 2.19.x (спека v0.65):
// первое совпавшее -- «Конфликт IP», потом «Нет связи» по проверке связи,
// потом слово по статусу. Элемент -- строка events.tunnels (проекция проверки),
// а не снимок маршрутов: туда withCheckVerdict пишет свой «dead».
const t = (over) => ({ tunnel_id: 'awg10', status: 'ok', ...over })

describe('awgmBadge -- правило карточки awg-manager', () => {
  it('без слова роутера -- строки нет', () => {
    expect(awgmBadge(t({}))).toBe(null)
    expect(awgmBadge(null)).toBe(null)
    expect(awgmBadge(undefined)).toBe(null)
  })

  it('конфликт адресов выше «Нет связи»', () => {
    const b = awgmBadge(t({ run_state: 'running', address_conflict: true, awgm_conn: 'disconnected' }))
    expect(b).toMatchObject({ label: 'Конфликт IP', tone: 'bad' })
  })

  it('running и связи нет -- «Нет связи»', () => {
    expect(awgmBadge(t({ run_state: 'running', awgm_conn: 'disconnected' }))).toEqual({ label: 'Нет связи', tone: 'bad', note: '' })
  })

  it('«Нет связи» с причиной -- причина в пояснении', () => {
    expect(awgmBadge(t({ run_state: 'running', awgm_conn: 'disconnected', awgm_conn_reason: 'timeout' }))).toEqual({
      label: 'Нет связи',
      tone: 'bad',
      note: 'timeout',
    })
  })

  it('running и ping-check восстанавливает -- «Восстанавливается»', () => {
    expect(awgmBadge(t({ run_state: 'running', ping_check_status: 'recovering', awgm_conn: 'connected' }))).toMatchObject({
      label: 'Восстанавливается',
      tone: 'warn',
    })
  })

  it('running и связь есть -- «Активен» с задержкой', () => {
    expect(awgmBadge(t({ run_state: 'running', awgm_conn: 'connected', awgm_conn_latency_ms: 64 }))).toEqual({
      label: 'Активен',
      tone: 'ok',
      note: '64 мс',
    })
  })

  // Старый агент или сборка awg-manager без проверки связи: молча «Активен»
  // было бы обещанием, которого никто не проверял.
  it('running без ответа о связи -- «Активен», связь не проверялась', () => {
    expect(awgmBadge(t({ run_state: 'running' }))).toEqual({ label: 'Активен', tone: 'ok', note: 'связь не проверялась' })
  })

  // Запрос упал -- это не «Нет связи»: awg-manager ничего не сказал.
  it('running и вопрос о связи сорвался -- «Активен», awg-manager не ответил', () => {
    expect(awgmBadge(t({ run_state: 'running', awgm_conn: 'error' }))).toEqual({
      label: 'Активен',
      tone: 'ok',
      note: 'awg-manager не ответил на проверку связи',
    })
  })

  it('broken -- «Сломан» с пояснением awg-manager', () => {
    expect(
      awgmBadge(t({ run_state: 'broken', awgm_conn: 'disconnected', awgm_status_details: 'Не запустился — перезапустите туннель' })),
    ).toEqual({ label: 'Сломан', tone: 'bad', note: 'Не запустился — перезапустите туннель' })
  })

  it('starting -- «Запускается»', () => {
    expect(awgmBadge(t({ run_state: 'starting' }))).toMatchObject({ label: 'Запускается', tone: 'warn' })
  })

  it('needs_stop и stopping -- «Останавливается»', () => {
    expect(awgmBadge(t({ run_state: 'needs_stop' }))).toMatchObject({ label: 'Останавливается', tone: 'warn' })
    expect(awgmBadge(t({ run_state: 'stopping' }))).toMatchObject({ label: 'Останавливается', tone: 'warn' })
  })

  it('needs_start и stopped -- «Остановлен»', () => {
    expect(awgmBadge(t({ run_state: 'needs_start' }))).toEqual({ label: 'Остановлен', tone: 'muted', note: '' })
    expect(awgmBadge(t({ run_state: 'stopped' }))).toEqual({ label: 'Остановлен', tone: 'muted', note: '' })
  })

  it('not_created -- «Не создан»', () => {
    expect(awgmBadge(t({ run_state: 'not_created' }))).toMatchObject({ label: 'Не создан', tone: 'muted' })
  })

  it('disabled -- «Выключен»', () => {
    expect(awgmBadge(t({ run_state: 'disabled' }))).toMatchObject({ label: 'Выключен', tone: 'muted' })
  })

  it('незнакомое слово -- оно само', () => {
    expect(awgmBadge(t({ run_state: 'weird_state' }))).toEqual({ label: 'weird_state', tone: 'muted', note: '' })
  })

  // «Нет связи» -- только у running, как в UI: у остановленного ответ о связи
  // не превращает «Остановлен» в ошибку.
  it('связь не перекрывает слово не-running', () => {
    expect(awgmBadge(t({ run_state: 'stopped', awgm_conn: 'disconnected' }))).toMatchObject({ label: 'Остановлен' })
  })
})
