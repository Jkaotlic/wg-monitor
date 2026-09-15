import { describe, it, expect } from 'vitest'
import {
  FLEET_UPDATE_PHRASE,
  agentUpdateState,
  agentUpdateErrorText,
  agentUpdateDoneText,
  agentCancelDoneText,
  agentUpdateSheetText,
  fleetUpdateTargets,
  fleetUpdateSheetText,
  fleetUpdateSummary,
  fleetUpdateErrorText,
  isAway,
} from '../src/agentUpdate.js'
import { ApiError } from '../src/api.js'

// Роутеры -- в форме miniappFleetRouter (miniapp_fleet.go) с полями частей 1-2.
const OFF_PENDING = {
  id: 11, nickname: 'bronya', status: 'offline', last_seen_age_sec: 345600,
  agent_version: 'v0.30.0', pending_version: 'v0.33.0', pending_attempts: 0,
  pending_last_error_text: '', agent_behind: true, agent_update_warning: '', notify_muted: false,
}
const ONLINE_TRYING = {
  id: 12, nickname: 'home', status: 'online', last_seen_age_sec: 30,
  agent_version: 'v0.32.0', pending_version: 'v0.33.0', pending_attempts: 2,
  pending_last_error_text: '', agent_behind: true, agent_update_warning: '', notify_muted: false,
}
const FAILING = {
  id: 13, nickname: 'dacha', status: 'online', last_seen_age_sec: 30,
  agent_version: 'v0.24.1', pending_version: 'v0.33.0', pending_attempts: 2,
  pending_last_error_text: 'на роутере не хватает места', agent_behind: true,
  agent_update_warning: 'старая проверка места: нужно ≈10% раздела /opt свободно', notify_muted: false,
}
const BEHIND = {
  id: 14, nickname: 'office', status: 'sleeping', last_seen_age_sec: 4000,
  agent_version: 'v0.17.2', pending_version: '', pending_attempts: 0,
  pending_last_error_text: '', agent_behind: true,
  agent_update_warning: 'проверяет адрес загрузки, должен совпасть с адресом бэкенда', notify_muted: true,
}
const FRESH = {
  id: 15, nickname: 'car', status: 'online', last_seen_age_sec: 20,
  agent_version: 'v0.33.0', pending_version: '', pending_attempts: 0,
  pending_last_error_text: '', agent_behind: false, agent_update_warning: '', notify_muted: false,
}
const GAVE_UP = { ...FRESH, id: 16, nickname: 'shed', agent_version: 'v0.31.0', agent_behind: true, pending_last_error_text: 'агент не скачал выпуск' }

const INTERNAL = /self_update|pending|version_audit|router_doctor|agent_behind|deploy_pending|confirm_mismatch/

describe('состояние обновления в строке роутера', () => {
  it('выключенный с отложенным обновлением -- ждёт включения, а не ошибка', () => {
    const s = agentUpdateState(OFF_PENDING)
    expect(s.text).toBe('ждёт включения: v0.33.0 поставится, когда роутер выйдет на связь')
    expect(s.tone).toBe('warn')
    expect(s.canCancel).toBe(true)
    expect(s.canUpdate).toBe(false)
  })

  it('спящий с отметкой -- тоже ждёт включения', () => {
    expect(agentUpdateState({ ...OFF_PENDING, status: 'sleeping' }).text).toContain('поставится, когда роутер выйдет на связь')
    // gachimikhail 15.09: статус alert, молчит 12 суток -- тоже «ждёт включения»
    expect(agentUpdateState({ ...OFF_PENDING, status: 'alert', last_seen_age_sec: 1040226 }).text).toContain('поставится, когда роутер выйдет на связь')
  })

  // Ledger #1: одна неудачная попытка, потом роутер выключили. Сервер
  // повторит при выходе на связь, значит строка -- «ждёт включения», а не
  // красное «не ставится». Причину прошлой попытки помним, но без тревоги.
  it('выключенный с причиной прошлой неудачи -- всё равно ждёт включения', () => {
    const s = agentUpdateState({ ...FAILING, status: 'offline', last_seen_age_sec: 345600 })
    expect(s.tone).toBe('warn')
    expect(s.text).toBe('ждёт включения: v0.33.0 поставится, когда роутер выйдет на связь · прошлая попытка: на роутере не хватает места')
    expect(s.text).not.toContain('не ставится')
    expect(s.canCancel).toBe(true)
    expect(s.canUpdate).toBe(false)
  })

  it('на связи и ставится -- говорит версию и число попыток с верным склонением', () => {
    expect(agentUpdateState(ONLINE_TRYING).text).toBe('ставится v0.33.0 · 2 попытки')
    expect(agentUpdateState({ ...ONLINE_TRYING, pending_attempts: 1 }).text).toBe('ставится v0.33.0 · 1 попытка')
    expect(agentUpdateState({ ...ONLINE_TRYING, pending_attempts: 5 }).text).toBe('ставится v0.33.0 · 5 попыток')
    expect(agentUpdateState({ ...ONLINE_TRYING, pending_attempts: 0 }).text).toBe('ставится v0.33.0')
  })

  it('последняя попытка сорвалась -- причина словами сервера и тон danger', () => {
    const s = agentUpdateState(FAILING)
    expect(s.text).toBe('не ставится v0.33.0: на роутере не хватает места · 2 попытки')
    expect(s.tone).toBe('danger')
    expect(s.canCancel).toBe(true)
  })

  it('отстаёт без отметки -- можно обновить, отменять нечего', () => {
    const s = agentUpdateState(BEHIND)
    expect(s.text).toBe('агент отстаёт от бэкенда')
    expect(s.canUpdate).toBe(true)
    expect(s.canCancel).toBe(false)
  })

  it('отметку сняли после неудач -- строка помнит причину и снова даёт обновить', () => {
    const s = agentUpdateState(GAVE_UP)
    expect(s.text).toBe('не поставилось: агент не скачал выпуск')
    expect(s.canUpdate).toBe(true)
  })

  it('свежий агент -- строки нет и кнопок нет', () => {
    expect(agentUpdateState(FRESH)).toEqual({ tone: 'ok', text: '', canUpdate: false, canCancel: false })
  })

  // Fix round 1, п.3 (review-minors-миниapp.md ⚠️ / final review B6): агент
  // ниже agentSelfUpdateFloor не отстаёт (agent_behind=false, B6) -- ветка
  // agent_behind не срабатывает, и раньше прошлая (уже неактивная) попытка
  // молча терялась, хотя сервер её отдаёт (miniapp_fleet.go verdict.TooOld).
  it('слишком старый агент, сдавшийся -- прошлая попытка не пропадает', () => {
    const s = agentUpdateState({
      ...FRESH, id: 20, nickname: 'antique', agent_behind: false, pending_version: '',
      pending_last_error_text: 'роутер не смог скачать обновление',
    })
    expect(s.text).toBe('прошлая попытка: роутер не смог скачать обновление')
    expect(s.tone).toBe('warn')
    expect(s.canUpdate).toBe(false)
    expect(s.canCancel).toBe(false)
  })

  it('ни одна строка не несёт внутренних имён', () => {
    for (const r of [OFF_PENDING, ONLINE_TRYING, FAILING, BEHIND, FRESH, GAVE_UP]) {
      expect(agentUpdateState(r).text).not.toMatch(INTERNAL)
    }
  })
})

// «На связи» решает сервер тем же правилом, что и отложенное обновление
// (final review M1). Поле away побеждает клиентский порог в обе стороны;
// без поля (старый бэкенд) остаётся прежнее правило.
describe('isAway', () => {
  it('поле сервера побеждает клиентский порог', () => {
    // статичный роутер в тревоге молчит 7 минут: сервер уже отложит обновление
    expect(isAway({ status: 'alert', last_seen_age_sec: 420, away: true })).toBe(true)
    // мобильный в тревоге молчит 20 минут: сервер ещё шлёт команду
    expect(isAway({ status: 'alert', last_seen_age_sec: 1200, away: false })).toBe(false)
    expect(isAway({ status: 'offline', last_seen_age_sec: 7200, away: false })).toBe(false)
  })

  it('без поля -- прежнее правило', () => {
    expect(isAway({ status: 'offline' })).toBe(true)
    expect(isAway({ status: 'alert', last_seen_age_sec: 1200 })).toBe(true)
    expect(isAway({ status: 'alert', last_seen_age_sec: 420 })).toBe(false)
    expect(isAway({ status: 'online', last_seen_age_sec: 5000 })).toBe(false)
  })
})

describe('отказы сервера словами', () => {
  const codes = ['confirm_mismatch', 'agent_too_old', 'deploy_pending', 'downgrade_rejected', 'no_release', 'not_found']
  it('у каждого кода контракта своя русская фраза без кода внутри', () => {
    for (const code of codes) {
      const text = agentUpdateErrorText(new ApiError(409, code, `/routers/1/agent/update failed: 409`))
      expect(text.length).toBeGreaterThan(10)
      expect(text).not.toMatch(INTERNAL)
      expect(text).not.toContain('failed')
    }
  })

  it('неизвестный код без фразы сервера -- пустая строка, лист скажет общее', () => {
    expect(agentUpdateErrorText(new ApiError(500, 'internal', 'x'))).toBe('')
    expect(agentUpdateErrorText(new Error('network'))).toBe('')
    expect(agentUpdateErrorText(undefined)).toBe('')
  })

  // not_configured прикрывает три разные причины на сервере (B5a): у базы, у
  // очереди, у публичного адреса. Свою фразу для этого кода клиент не
  // заводит -- иначе различие потерялось бы. bad_request и internal -- тоже
  // общие коды с фразой сервера, а не своей.
  it('not_configured/bad_request/internal -- показывает фразу сервера, а не одну на все причины', () => {
    const dbDown = new ApiError(503, 'not_configured', 'x failed: 503', 'У сервера не настроена база данных.')
    const noPublicAddr = new ApiError(503, 'not_configured', 'x failed: 503', 'У сервера не задан публичный адрес: обновлению неоткуда скачаться.')
    expect(agentUpdateErrorText(dbDown)).toBe('У сервера не настроена база данных.')
    expect(agentUpdateErrorText(noPublicAddr)).toBe('У сервера не задан публичный адрес: обновлению неоткуда скачаться.')
    expect(agentUpdateErrorText(dbDown)).not.toBe(agentUpdateErrorText(noPublicAddr))
    expect(agentUpdateErrorText(new ApiError(400, 'bad_request', 'x failed: 400', 'Не удалось прочитать запрос.'))).toBe(
      'Не удалось прочитать запрос.',
    )
    expect(agentUpdateErrorText(new ApiError(500, 'internal', 'x failed: 500', 'Не удалось назначить обновление.'))).toBe(
      'Не удалось назначить обновление.',
    )
  })

  // Fix round 1, Important #2 (review-minors-miniapp.md): serverMessage
  // раньше показывался для ЛЮБОГО незнакомого кода -- а сессия истекает не
  // в хендлерах обновления, а в MiniAppAuthMiddleware (miniapp_auth.go:198),
  // и её message английский: "sign in required". allowlist ограничивает
  // фразу сервера кодами обновления агента (not_configured/bad_request/
  // internal), у 401 -- своя русская фраза, у остального -- пусто.
  it('401 (сессия истекла) -- русская фраза, а не message middleware', () => {
    const expired = new ApiError(401, 'unauthorized', '/routers/1/agent/update failed: 401', 'sign in required')
    expect(agentUpdateErrorText(expired)).toBe('Сессия истекла — откройте приложение заново.')
    expect(fleetUpdateErrorText(expired)).toBe('Сессия истекла — откройте приложение заново.')
    expect(agentUpdateErrorText(expired)).not.toMatch(/sign in/)
  })

  it('код вне контракта обновления -- пустая строка, даже если у него есть message', () => {
    const foreign = new ApiError(500, 'some_other_middleware_code', 'x failed: 500', 'unexpected debug text')
    expect(agentUpdateErrorText(foreign)).toBe('')
    expect(fleetUpdateErrorText(foreign)).toBe('')
  })

  it('старый агент -- говорит, что нужна переустановка', () => {
    expect(agentUpdateErrorText({ code: 'agent_too_old' })).toContain('переустановить')
  })
})

describe('итог одного обновления', () => {
  it('deferred -- поставится, когда роутер выйдет на связь', () => {
    expect(agentUpdateDoneText({ queued: true, deferred: true, target_version: 'v0.33.0' }, 'bronya')).toBe(
      'Обновление «bronya» до v0.33.0 поставится, когда роутер выйдет на связь.',
    )
  })

  it('на связи -- отправлено, версия сменится после отчёта', () => {
    expect(agentUpdateDoneText({ queued: true, deferred: false, target_version: 'v0.33.0' }, 'home')).toBe(
      'Обновление «home» до v0.33.0 отправлено на роутер. Новая версия появится после его следующего отчёта.',
    )
  })

  it('отмена: снято и «снимать было нечего» -- разные фразы, обе не ошибка', () => {
    expect(agentCancelDoneText({ cleared: true }, 'bronya')).toBe('Обновление «bronya» отменено.')
    expect(agentCancelDoneText({ cleared: false }, 'bronya')).toBe('Отменять было нечего: обновление «bronya» уже поставилось или снято.')
  })
})

describe('лист обновления одного роутера', () => {
  it('называет версии «с» и «до»', () => {
    const t = agentUpdateSheetText(BEHIND, 'v0.33.0')
    expect(t.title).toBe('Обновить агент на «office»?')
    expect(t.body).toContain('с v0.17.2 до v0.33.0')
  })

  it('спящий или выключенный -- обещает постановку при выходе на связь', () => {
    expect(agentUpdateSheetText(BEHIND, 'v0.33.0').body).toContain('обновление поставится, когда он выйдет на связь')
    expect(agentUpdateSheetText({ ...BEHIND, status: 'online' }, 'v0.33.0').body).not.toContain('выйдет на связь')
  })

  it('оговорку сервера пересказывает дословно', () => {
    expect(agentUpdateSheetText(BEHIND, 'v0.33.0').body).toContain('Оговорка: проверяет адрес загрузки, должен совпасть с адресом бэкенда.')
    expect(agentUpdateSheetText({ ...BEHIND, agent_update_warning: '' }, 'v0.33.0').body).not.toContain('Оговорка')
    expect(agentUpdateSheetText({ ...BEHIND, agent_update_warning: 'нужно место.' }, 'v0.33.0').body).toContain('Оговорка: нужно место. Агент')
  })

  it('без известной версии агента -- только «до»', () => {
    expect(agentUpdateSheetText({ ...BEHIND, agent_version: '' }, 'v0.33.0').body).toContain('обновится до v0.33.0')
  })
})

describe('обновить всех отставших', () => {
  const fleet = { backend: { version: 'v0.33.0' }, routers: [OFF_PENDING, ONLINE_TRYING, FAILING, BEHIND, FRESH, GAVE_UP] }

  it('цели -- отстающие без отметки', () => {
    expect(fleetUpdateTargets(fleet).map((r) => r.id)).toEqual([14, 16])
    expect(fleetUpdateTargets({})).toEqual([])
  })

  it('слово подтверждения -- «обновить»', () => {
    expect(FLEET_UPDATE_PHRASE).toBe('обновить')
  })

  it('лист называет число, имена и что выключенные получат позже', () => {
    const t = fleetUpdateSheetText(fleet)
    expect(t.title).toBe('Обновить агент на всех отставших?')
    expect(t.body).toContain('Отстают 2 роутера: «office», «shed».')
    expect(t.body).toContain('Выключенные и спящие получат обновление, когда выйдут на связь.')
    expect(fleetUpdateSheetText({ routers: [BEHIND] }).body).toContain('Отстаёт 1 роутер: «office».')
  })

  it('итог по исходам, с причинами словами сервера и без кодов', () => {
    const s = fleetUpdateSummary([
      { router_id: 14, nickname: 'office', outcome: 'queued', reason_code: '', reason_text: '' },
      { router_id: 11, nickname: 'bronya', outcome: 'deferred', reason_code: '', reason_text: '' },
      { router_id: 17, nickname: 'garage', outcome: 'deferred', reason_code: '', reason_text: '' },
      { router_id: 18, nickname: 'old', outcome: 'skipped', reason_code: 'agent_too_old', reason_text: 'агент слишком старый — только переустановка' },
      { router_id: 19, nickname: 'x', outcome: 'error', reason_code: 'internal', reason_text: '' },
    ])
    expect(s.headline).toBe('Обновление: поставлено 1, ждут включения 2, пропущен 1, не получилось 1.')
    // Ключ строки -- router_id: два роутера могут дать одинаковый текст
    // («поставится, когда роутер выйдет на связь»), а ключи должны остаться разными.
    expect(s.lines).toEqual([
      { id: 11, text: '«bronya»: поставится, когда роутер выйдет на связь' },
      { id: 17, text: '«garage»: поставится, когда роутер выйдет на связь' },
      { id: 18, text: '«old»: пропущен — агент слишком старый — только переустановка' },
      { id: 19, text: '«x»: не получилось — ошибка на сервере' },
    ])
    expect(s.lines.map((l) => l.text).join(' ')).not.toMatch(INTERNAL)
  })

  it('склонения в итоге', () => {
    const one = fleetUpdateSummary([{ router_id: 1, nickname: 'a', outcome: 'deferred', reason_code: '', reason_text: '' }])
    expect(one.headline).toBe('Обновление: ждёт включения 1.')
    const skipped = fleetUpdateSummary([
      { router_id: 1, nickname: 'a', outcome: 'skipped', reason_code: 'x', reason_text: 'r' },
      { router_id: 2, nickname: 'b', outcome: 'skipped', reason_code: 'x', reason_text: 'r' },
    ])
    expect(skipped.headline).toBe('Обновление: пропущено 2.')
  })

  it('неверное слово -- своя фраза; прочие отказы -- фраза сервера, если есть', () => {
    expect(fleetUpdateErrorText({ code: 'confirm_mismatch' })).toBe('Слово «обновить» набрано неверно — ничего не поставлено.')
    expect(fleetUpdateErrorText({ code: 'internal' })).toBe('')
    expect(
      fleetUpdateErrorText(new ApiError(500, 'internal', 'x failed: 500', 'Не удалось назначить обновление.')),
    ).toBe('Не удалось назначить обновление.')
  })

  it('пустой итог -- «обновлять некого», а не пустая строка', () => {
    expect(fleetUpdateSummary([])).toEqual({ headline: 'Отставших нет — обновлять некого.', lines: [] })
    expect(fleetUpdateSummary(undefined)).toEqual({ headline: 'Отставших нет — обновлять некого.', lines: [] })
  })
})
