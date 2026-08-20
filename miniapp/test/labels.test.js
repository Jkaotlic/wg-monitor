import { describe, it, expect } from 'vitest'
import { commandOutcomeLabel } from '../src/labels.js'

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
    expect(commandOutcomeLabel('route_rebind', ok(out))).toBe('Переносить было нечего: правил на этом туннеле нет')
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
    expect(commandOutcomeLabel('route_policy_promote', ok(out))).toBe('Правила политики «HydraRoute» идут через «main»')
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
