import { describe, it, expect } from 'vitest'
import {
  DEPLOY_POLL_MS,
  DEPLOY_TIMEOUT_MS,
  DEPLOY_RELOAD_MS,
  sameVersion,
  backendDeployOffer,
  backendDeploySheetText,
  backendDeployErrorText,
  deployWaitStart,
  deployWaitStep,
  deployWaitText,
  watchBackendDeploy,
} from '../src/backendDeploy.js'
import { ApiError } from '../src/api.js'

const T0 = 1_000_000

describe('предложение раскатки', () => {
  it('только когда сервер сказал «есть новее» и назвал версию', () => {
    expect(backendDeployOffer({ backend: { version: 'v0.35.0', latest_version: 'v0.36.0', update_available: true } })).toEqual({
      target: 'v0.36.0',
      label: 'Обновить бэкенд до v0.36.0',
    })
    expect(backendDeployOffer({ backend: { version: 'v0.36.0', latest_version: 'v0.36.0', update_available: false } })).toBe(null)
    expect(backendDeployOffer({ backend: { update_available: true, latest_version: '' } })).toBe(null)
    expect(backendDeployOffer(null)).toBe(null)
  })

  it('текст листа', () => {
    expect(backendDeploySheetText('v0.36.0')).toEqual({
      title: 'Обновить бэкенд до v0.36.0?',
      body: 'Сервер скачает v0.36.0 и перезапустится. Пока он перезапускается, приложение не отвечает — обычно минуту-две. Откатить бэкенд из приложения нельзя.',
    })
  })

  it('ошибки: фраза сервера, затем своя по коду, затем пусто (лист скажет общее)', () => {
    expect(backendDeployErrorText(new ApiError(400, 'confirm_mismatch', 'x', 'Подтверждение не совпало (сервер)'))).toBe('Подтверждение не совпало (сервер)')
    expect(backendDeployErrorText(new ApiError(400, 'confirm_mismatch', 'x'))).toBe('Подтверждение не совпало')
    expect(backendDeployErrorText(new ApiError(503, 'backend_update_not_configured', 'x'))).toBe('Раскатка бэкенда на этом сервере не настроена')
    expect(backendDeployErrorText(new Error('net'))).toBe('')
  })
})

describe('сравнение версий', () => {
  it('без буквы v и пробелов', () => {
    expect(sameVersion('v0.36.0', '0.36.0')).toBe(true)
    expect(sameVersion(' v0.36.0 ', 'v0.36.0')).toBe(true)
    expect(sameVersion('v0.35.0', 'v0.36.0')).toBe(false)
    expect(sameVersion('', '')).toBe(false)
  })
})

describe('машина ожидания', () => {
  it('прежняя версия -- ждём; ошибка -- перезапуск; новая -- готово', () => {
    let s = deployWaitStart('v0.36.0', T0)
    expect(s).toEqual({ phase: 'waiting', target: 'v0.36.0', startedAt: T0, lastVersion: '' })
    s = deployWaitStep(s, { type: 'health', version: 'v0.35.0', at: T0 + 3000 })
    expect(s.phase).toBe('waiting')
    expect(s.lastVersion).toBe('v0.35.0')
    s = deployWaitStep(s, { type: 'down', at: T0 + 6000 })
    expect(s.phase).toBe('restarting')
    s = deployWaitStep(s, { type: 'health', version: '0.36.0', at: T0 + 9000 })
    expect(s.phase).toBe('done')
    expect(deployWaitStep(s, { type: 'down', at: T0 + 12000 })).toBe(s)
  })

  it('5 минут без новой версии -- таймаут, и он окончателен', () => {
    let s = deployWaitStart('v0.36.0', T0)
    s = deployWaitStep(s, { type: 'down', at: T0 + DEPLOY_TIMEOUT_MS - 1 })
    expect(s.phase).toBe('restarting')
    s = deployWaitStep(s, { type: 'health', version: 'v0.35.0', at: T0 + DEPLOY_TIMEOUT_MS })
    expect(s.phase).toBe('timeout')
    expect(deployWaitStep(s, { type: 'health', version: 'v0.36.0', at: T0 + DEPLOY_TIMEOUT_MS + 3000 })).toBe(s)
  })

  it('новая версия ровно на пятой минуте -- всё-таки готово', () => {
    const s = deployWaitStep(deployWaitStart('v0.36.0', T0), { type: 'health', version: 'v0.36.0', at: T0 + DEPLOY_TIMEOUT_MS })
    expect(s.phase).toBe('done')
  })

  it('слова по фазам', () => {
    const start = deployWaitStart('v0.36.0', T0)
    expect(deployWaitText(start)).toEqual({ title: 'Бэкенд обновляется до v0.36.0', line: 'Ждём ответа сервера…', tone: 'running' })
    expect(deployWaitText({ ...start, lastVersion: 'v0.35.0' }).line).toBe('Сервер ещё отвечает прежней версией v0.35.0 — скачивает новую…')
    expect(deployWaitText({ ...start, phase: 'restarting' })).toEqual({ title: 'Бэкенд обновляется до v0.36.0', line: 'Сервер перезапускается…', tone: 'running' })
    expect(deployWaitText({ ...start, phase: 'done' })).toEqual({ title: 'Готово, бэкенд v0.36.0', line: 'Перезагружаем страницу…', tone: 'ok' })
    expect(deployWaitText({ ...start, phase: 'timeout' })).toEqual({
      title: 'Бэкенд не ответил новой версией за 5 минут',
      line: 'Проверьте сводку позже.',
      tone: 'bad',
    })
  })

  it('константы спеки', () => {
    expect(DEPLOY_POLL_MS).toBe(3000)
    expect(DEPLOY_TIMEOUT_MS).toBe(300000)
    expect(DEPLOY_RELOAD_MS).toBe(2000)
  })
})

describe('watchBackendDeploy', () => {
  function clock() {
    let t = T0
    const sleeps = []
    return { now: () => t, sleep: async (ms) => { sleeps.push(ms); t += ms }, sleeps }
  }

  it('опрашивает раз в 3 с: ошибки и 5xx -- норма, до новой версии', async () => {
    const c = clock()
    const replies = [{ version: 'v0.35.0' }, new Error('net'), new ApiError(502, 'unknown', 'x'), { version: 'v0.36.0' }]
    const phases = []
    const final = await watchBackendDeploy({
      target: 'v0.36.0',
      fetchHealth: async () => {
        const r = replies.shift()
        if (r instanceof Error) throw r
        return r
      },
      sleep: c.sleep,
      now: c.now,
      onState: (s) => phases.push(s.phase),
      signal: { cancelled: false },
    })
    expect(final.phase).toBe('done')
    expect(phases).toEqual(['waiting', 'waiting', 'restarting', 'restarting', 'done'])
    expect(c.sleeps).toEqual([3000, 3000, 3000])
  })

  it('без новой версии -- таймаут через 5 минут', async () => {
    const c = clock()
    let asked = 0
    const final = await watchBackendDeploy({
      target: 'v0.36.0',
      fetchHealth: async () => {
        asked++
        return { version: 'v0.35.0' }
      },
      sleep: c.sleep,
      now: c.now,
      onState: () => {},
      signal: { cancelled: false },
    })
    expect(final.phase).toBe('timeout')
    expect(asked).toBe(101)
  })

  it('отмена останавливает опрос', async () => {
    const signal = { cancelled: false }
    let asked = 0
    await watchBackendDeploy({
      target: 'v0.36.0',
      fetchHealth: async () => {
        asked++
        return { version: 'v0.35.0' }
      },
      sleep: async () => { signal.cancelled = true },
      now: () => T0,
      onState: () => {},
      signal,
    })
    expect(asked).toBe(1)
  })
})
