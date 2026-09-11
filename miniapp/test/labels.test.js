import { describe, it, expect } from 'vitest'
import { commandOutcomeLabel, checkLabel, eventPhrase, legendLabel, incidentCopy } from '../src/labels.js'

// Результат маршрутной команды -- это JSON агента (pkg/wire/routing.go), а не
// строка для человека. "Готово" на нём было бы враньём в двух случаях сразу:
// перенос умеет пройти наполовину, а повышение звена -- не сдвинуть трафик.
describe('commandOutcomeLabel: маршруты', () => {
  const ok = (output) => ({ status: 'ok', output })

  it('перенос называет число перенесённых правил', () => {
    const out = JSON.stringify({
      src_tunnel_id: 'awg11', dst_tunnel_id: 'awg10',
      dns: { ok: 24, failed: 0 }, static: { ok: 2, failed: 0 }, hr_neo: { ok: 24, failed: 0 },
    })
    expect(commandOutcomeLabel('route_rebind', ok(out))).toBe('Перенесено 26 правил')
  })

  it('частичный перенос не выдаётся за успех', () => {
    const out = JSON.stringify({
      dns: { ok: 24, failed: 2, errors: ['dns/route/17: 500'] }, static: { ok: 0, failed: 0 }, hr_neo: { ok: 0, failed: 0 },
    })
    const text = commandOutcomeLabel('route_rebind', ok(out))
    expect(text).toContain('24')
    expect(text).toContain('2')
    expect(text).toMatch(/не удалось/i)
  })

  it('переносить было нечего -- тоже ответ', () => {
    const out = JSON.stringify({ dns: { ok: 0, failed: 0 }, static: { ok: 0, failed: 0 }, hr_neo: { ok: 0, failed: 0 } })
    expect(commandOutcomeLabel('route_rebind', ok(out))).toBe('Переносить было нечего: правил на этом VPN-туннеле нет')
  })

  it('повышение звена называет и порядок, и того, кто несёт трафик', () => {
    const out = JSON.stringify({
      name: 'HydraRoute', active_tunnel_id: 'awg11',
      interfaces: [
        { bind: 'OpkgTun10', name: 'main', role: 'unavailable', tunnel_id: 'awg10' },
        { bind: 'OpkgTun11', name: 'work', role: 'active', tunnel_id: 'awg11' },
      ],
    })
    const text = commandOutcomeLabel('route_policy_promote', ok(out))
    expect(text).toContain('main')
    expect(text).toContain('work')
    expect(text).toMatch(/пока идёт/i)
  })

  it('повышение сдвинуло трафик -- говорим коротко', () => {
    const out = JSON.stringify({
      name: 'HydraRoute', active_tunnel_id: 'awg10',
      interfaces: [{ bind: 'OpkgTun10', name: 'main', role: 'active', tunnel_id: 'awg10' }],
    })
    expect(commandOutcomeLabel('route_policy_promote', ok(out))).toBe('Правила общего набора «HydraRoute» идут через «main»')
  })

  // Словарь: политика -- это «общий набор правил», звено цепочки человеку не
  // адресовано. Итог повышения печатается сразу после нажатия, и все три его
  // ветки обязаны говорить так же, как остальной экран.
  it('итог повышения не говорит «политика» и «звено» ни в одной ветке', () => {
    const branches = [
      // первым встал выключенный, трафик ни через кого не идёт
      { name: 'HydraRoute', active_tunnel_id: '', interfaces: [{ bind: 'OpkgTun10', name: 'main', tunnel_id: 'awg10' }] },
      // первым встал один, трафик пока через другого
      { name: 'HydraRoute', active_tunnel_id: 'awg11', interfaces: [
        { bind: 'OpkgTun10', name: 'main', tunnel_id: 'awg10' },
        { bind: 'OpkgTun11', name: 'work', tunnel_id: 'awg11' },
      ] },
      // трафик сдвинулся
      { name: 'HydraRoute', active_tunnel_id: 'awg10', interfaces: [{ bind: 'OpkgTun10', name: 'main', tunnel_id: 'awg10' }] },
    ]
    for (const b of branches) {
      const text = commandOutcomeLabel('route_policy_promote', ok(JSON.stringify(b)))
      expect(text).toContain('«HydraRoute»')
      expect(text).not.toMatch(/политик|звен/i)
    }
  })

  it('правило создано -- называем его именем', () => {
    const out = JSON.stringify({ action: 'add', kind: 'dns', route_id: 'hr:AI', route_name: 'AI' })
    expect(commandOutcomeLabel('route_add', ok(out))).toBe('Правило «AI» создано')
  })

  // Агент применяет правку и отдельно сообщает, что после неё не удалось
  // обновить маршрутизацию. Проглотить это значило бы сказать "готово" о
  // роутере, который живёт по старой таблице.
  it('оговорка агента доезжает до человека', () => {
    const out = JSON.stringify({ action: 'delete', route_name: 'AI', warning: 'post-change refresh failed' })
    const text = commandOutcomeLabel('route_delete', ok(out))
    expect(text).toContain('AI')
    expect(text).toContain('post-change refresh failed')
  })

  // Старый агент или чужой ответ: разбирать нечего, но и врать нечем.
  it('неразбираемый ответ не превращается в выдумку', () => {
    expect(commandOutcomeLabel('route_rebind', ok('готово'))).toBe('Готово')
  })
})

// Флот разноверсионный: агент на чужом роутере может быть старше приложения
// и такого действия не знать вовсе. Его ответ -- «unknown action: ...» --
// человеку ничего не объясняет: он не виноват, что там старый агент, и
// «unknown action» читается как поломка приложения.
describe('commandOutcomeLabel: старый агент', () => {
  it('незнакомое действие объясняется версией агента, а не кодом', () => {
    const text = commandOutcomeLabel('tunnel_traffic', { status: 'err', output: 'unknown action: tunnel_traffic' })
    expect(text).toContain('агент')
    expect(text).not.toContain('unknown action')
  })

  it('обычная ошибка агента по-прежнему доезжает как есть', () => {
    const text = commandOutcomeLabel('tunnel_traffic', { status: 'err', output: 'awgmgr tunnels/traffic: success=false' })
    expect(text).toBe('awgmgr tunnels/traffic: success=false')
  })
})

// agent_heartbeat агент присылает в КАЖДОМ отчёте (internal/agent/reporter.go),
// но человеческой подписи у него не было ни в одной из карт: журнал писал
// «agent_heartbeat — снова в норме», легенда на корпусе подписывала лампу
// идентификатором, а карточка тревоги -- именем механизма вместо последствия.
// Имя проверки -- это идентификатор, и показывать его человеку значит
// перекладывать перевод на него.
describe('agent_heartbeat говорит по-человечески', () => {
  it('в списке проверок', () => {
    expect(checkLabel('agent_heartbeat')).toBe('Отчёты от роутера')
  })

  it('в журнале событий', () => {
    expect(eventPhrase('agent_heartbeat', 'ok')).toBe('Роутер снова выходит на связь')
    expect(eventPhrase('agent_heartbeat', 'fail')).not.toContain('agent_heartbeat')
  })

  it('в легенде на корпусе', () => {
    expect(legendLabel('agent_heartbeat')).toBe('отчёты агента')
  })

  it('в карточке тревоги -- последствием, а не именем механизма', () => {
    const copy = incidentCopy('agent_heartbeat')
    expect(copy.what).toBe('Роутер не выходит на связь')
    expect(copy.why).toContain('отчёт')
  })
})

// resolver_guard -- сторож своего DNS-сервера (спека dns-watchdog). Бот
// говорит о нём «Свой DNS-сервер», и приложение обязано говорить так же:
// два голоса одной системы -- одни слова. Причины карточка не знает (у
// инцидента только имя проверки), а их три: роутер ушёл на запасные, запасные
// недоступны, запасные не снялись рядом с отвечающим своим. Поэтому «что»
// не утверждает, что свой сервер молчит, а «почему» честно называет все три.
describe('resolver_guard говорит «Свой DNS-сервер»', () => {
  const jargon = ['DoH', 'апстрим', 'резолвинг', 'resolver_guard']

  it('в списке проверок', () => {
    expect(checkLabel('resolver_guard')).toBe('Свой DNS-сервер')
  })

  it('в карточке тревоги -- что случилось и чем грозит', () => {
    const copy = incidentCopy('resolver_guard')
    expect(copy.what).toBe('Неполадка с DNS-серверами роутера')
    expect(copy.why).toContain('временно перешёл на запасные')
    expect(copy.why).toContain('сайты по имени могут не открываться')
    // Слова бота для третьей причины (alerts/format.go, foreign_leftover).
    expect(copy.why).toContain('запасные DNS-серверы не снялись — сайты открываются, но часть запросов идёт мимо фильтров')
    for (const w of jargon) expect(copy.what + ' ' + copy.why).not.toContain(w)
  })

  it('в журнале событий', () => {
    // Верно при любой из трёх причин: и когда роутер уходил на запасные, и
    // когда оставался на молчащем своём, и когда свой отвечал всё время.
    expect(eventPhrase('resolver_guard', 'ok')).toBe('Роутер снова работает через свой DNS-сервер')
    expect(eventPhrase('resolver_guard', 'fail')).toBe('Неполадка с DNS-серверами роутера')
    for (const phrase of [eventPhrase('resolver_guard', 'ok'), eventPhrase('resolver_guard', 'fail')]) {
      expect(phrase).not.toContain('не отвечает')
      expect(phrase).not.toContain('снова отвечает')
    }
  })
})
