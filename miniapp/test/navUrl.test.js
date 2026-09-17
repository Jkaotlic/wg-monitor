import { describe, it, expect } from 'vitest'
import { navFromURL, urlFromNav } from '../src/navUrl.js'
import { TABS, OPEN_OVERLAYS } from '../src/nav.js'

const IDS = [3, 7, 12]
const pick = (s) => ({ routerID: s.routerID, tab: s.tab, overlay: s.overlay, sheet: s.sheet ?? null })

describe('urlFromNav', () => {
  it('роутер не выбран -- пустой адрес', () => {
    expect(urlFromNav({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })).toBe('')
  })

  it('вкладка «Сейчас» не пишется, остальные пишутся', () => {
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: null, sheet: null })).toBe('?router=7')
    expect(urlFromNav({ routerID: 7, tab: 'tunnels', overlay: null, sheet: null })).toBe('?router=7&tab=tunnels')
  })

  it('оверлей роутера пишется, лист и список роутеров -- нет', () => {
    expect(urlFromNav({ routerID: 7, tab: 'diag', overlay: 'admin', sheet: { title: 'Точно?' } })).toBe('?router=7&tab=diag&open=admin')
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'fleet', sheet: null })).toBe('?router=7')
  })
})

describe('navFromURL', () => {
  it('пустой адрес при нескольких роутерах -- список', () => {
    expect(pick(navFromURL('', IDS))).toEqual({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })
  })

  it('чужой роутер не открывается, вкладка и open при этом игнорируются', () => {
    expect(pick(navFromURL('?router=99&tab=diag&open=admin', IDS))).toEqual({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })
  })

  it('неизвестные tab и open игнорируются', () => {
    expect(pick(navFromURL('?router=7&tab=hack&open=evil', IDS))).toEqual({ routerID: 7, tab: 'router', overlay: null, sheet: null })
  })

  it('старый псевдоним tab=routes ведёт на VPN-туннели', () => {
    expect(navFromURL('?router=7&tab=routes', IDS).tab).toBe('tunnels')
  })

  it('единственный роутер открывается и без router=', () => {
    expect(pick(navFromURL('?tab=events', [5]))).toEqual({ routerID: 5, tab: 'events', overlay: null, sheet: null })
  })

  it('мусор в router= -- как без него', () => {
    expect(navFromURL('?router=abc', IDS).routerID).toBe(null)
  })

  it('лист из адреса не открывается никогда', () => {
    expect(navFromURL('?router=7&sheet=reboot', IDS).sheet).toBe(null)
  })
})

describe('круговое свойство', () => {
  it('адрес -> навигация -> адрес сохраняет роутер, вкладку и оверлей', () => {
    const states = [{ routerID: null, tab: 'router', overlay: 'fleet', sheet: null }]
    for (const routerID of IDS) {
      for (const tab of TABS) {
        for (const overlay of [null, ...OPEN_OVERLAYS]) states.push({ routerID, tab, overlay, sheet: null })
      }
    }
    for (const s of states) {
      expect(pick(navFromURL(urlFromNav(s), IDS)), JSON.stringify(s)).toEqual(s)
      expect(urlFromNav(navFromURL(urlFromNav(s), IDS))).toBe(urlFromNav(s))
    }
  })

  it('открытый лист теряется при круге -- обновление страницы не повторяет подтверждение', () => {
    const s = { routerID: 7, tab: 'router', overlay: 'settings', sheet: { title: 'Перезагрузить?' } }
    expect(navFromURL(urlFromNav(s), IDS).sheet).toBe(null)
  })
})

describe('слои без адреса', () => {
  it('мастер и ход работы в адрес не пишутся -- пишется слой, откуда их открыли', () => {
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'provision', overlayParams: { returnTo: 'admin' }, sheet: null })).toBe('?router=7&open=admin')
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'job', overlayParams: { jobId: 'secret-job', title: 'x', returnTo: null }, sheet: null })).toBe('?router=7')
    expect(urlFromNav({ routerID: null, tab: 'router', overlay: 'backenddeploy', overlayParams: { targetVersion: 'v0.36.0' }, sheet: null })).toBe('')
  })

  it('returnTo не из списка адресов не пишется', () => {
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'job', overlayParams: { returnTo: 'provision' }, sheet: null })).toBe('?router=7')
  })

  it('подключение агента -- в адресе и открывается по нему', () => {
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'agentconn', sheet: null })).toBe('?router=7&open=agentconn')
    expect(navFromURL('?router=7&open=agentconn', IDS).overlay).toBe('agentconn')
    expect(navFromURL('?router=7&open=provision', IDS).overlay).toBe(null)
  })
})

describe('кабинет и свои серверы в адресе', () => {
  it('кабинет роутера пишется и читается', () => {
    expect(urlFromNav({ routerID: 7, tab: 'tunnels', overlay: 'cabinet', sheet: null })).toBe('?router=7&tab=tunnels&open=cabinet')
    const s = navFromURL('?router=7&tab=tunnels&open=cabinet', IDS)
    expect(pick(s)).toEqual({ routerID: 7, tab: 'tunnels', overlay: 'cabinet', sheet: null })
  })

  it('свои серверы без роутера -- ?open=selfhosted, возврат к сводке', () => {
    expect(urlFromNav({ routerID: null, tab: 'router', overlay: 'selfhosted', overlayParams: { returnTo: null }, sheet: null })).toBe('?open=selfhosted')
    const s = navFromURL('?open=selfhosted', IDS)
    expect(pick(s)).toEqual({ routerID: null, tab: 'router', overlay: 'selfhosted', sheet: null })
    expect(s.overlayParams).toEqual({ returnTo: null })
  })

  it('свои серверы с роутером -- возврат в «Обслуживание»', () => {
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'selfhosted', overlayParams: { returnTo: 'admin' }, sheet: null })).toBe('?router=7&open=selfhosted')
    const s = navFromURL('?router=7&open=selfhosted', IDS)
    expect(pick(s)).toEqual({ routerID: 7, tab: 'router', overlay: 'selfhosted', sheet: null })
    expect(s.overlayParams).toEqual({ returnTo: 'admin' })
  })

  it('экран сервера в адрес не пишется -- остаётся список; id сервера в адресе нет', () => {
    const inst = { routerID: null, tab: 'router', overlay: 'selfhostedinst', overlayParams: { instanceId: 'ams', returnTo: 'selfhosted', returnParams: { returnTo: null } }, sheet: null }
    expect(urlFromNav(inst)).toBe('?open=selfhosted')
    expect(urlFromNav({ ...inst, routerID: 7 })).toBe('?router=7&open=selfhosted')
    expect(navFromURL('?open=selfhostedinst', IDS).overlay).toBe('fleet')
  })

  it('один роутер: ?open=selfhosted открывает список поверх него', () => {
    const s = navFromURL('?open=selfhosted', [5])
    expect(pick(s)).toEqual({ routerID: 5, tab: 'router', overlay: 'selfhosted', sheet: null })
    expect(s.overlayParams).toEqual({ returnTo: 'admin' })
  })

  it('круг для своих серверов', () => {
    for (const s of [
      { routerID: null, tab: 'router', overlay: 'selfhosted', overlayParams: { returnTo: null }, sheet: null },
      { routerID: 7, tab: 'diag', overlay: 'selfhosted', overlayParams: { returnTo: 'admin' }, sheet: null },
    ]) {
      expect(urlFromNav(navFromURL(urlFromNav(s), IDS))).toBe(urlFromNav(s))
    }
  })
})
