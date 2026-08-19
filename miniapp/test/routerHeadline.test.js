import { describe, it, expect } from 'vitest'
import { routerHeadline } from '../src/routerHeadline.js'

const ONLINE = { status: 'online', nickname: 'testkeen', last_seen_age_sec: 52 }

describe('routerHeadline', () => {
  // Во главе экрана -- работа туннелей. Службы роутера объясняют картину, но
  // не они её задают.
  it('туннель несёт трафик -- тёплая шапка и имя туннеля в выводе', () => {
    const h = routerHeadline({
      router: ONLINE,
      traffic: { mode: 'vpn', egress_tunnel_name: 'awg3-work-via-ru1' },
      incidents: [],
    })
    expect(h.cold).toBe(false)
    expect(h.tone).toBe('sig')
    expect(h.tag).toBe('туннель поднят · ответ 52 сек назад')
    expect(h.verdict).toContain('awg3-work-via-ru1')
  })

  // Трафик напрямую -- это не авария, но и не норма: обход блокировок не
  // работает, и сказать об этом надо словом, а не отсутствием слова.
  it('трафик напрямую -- предупреждение, а не тишина', () => {
    const h = routerHeadline({ router: ONLINE, traffic: { mode: 'direct' }, incidents: [] })
    expect(h.tone).toBe('warn')
    expect(h.cold).toBe(true)
    expect(h.verdict).toContain('напрямую')
  })

  // Молчащий роутер важнее любого показания: всё остальное на экране в этот
  // момент -- вчерашние данные, и выдавать их за текущие нельзя.
  it('роутер молчит -- это перебивает любую другую новость', () => {
    const h = routerHeadline({
      router: { status: 'offline', nickname: 'testkeen', last_seen_age_sec: 900 },
      traffic: { mode: 'vpn', egress_tunnel_name: 'awg11' },
      incidents: [{ check_name: 'dns' }],
    })
    expect(h.tone).toBe('danger')
    expect(h.cold).toBe(true)
    expect(h.tag).toContain('не отвечает')
    expect(h.verdict).toContain('15 мин')
  })

  it('открытая тревога на живом роутере называет, что именно сломано', () => {
    const h = routerHeadline({
      router: ONLINE,
      traffic: { mode: 'vpn', egress_tunnel_name: 'awg11' },
      incidents: [{ check_name: 'hydraroute' }],
    })
    expect(h.tone).toBe('danger')
    expect(h.cold).toBe(true)
    expect(h.tag).toBe('не работает обход блокировок')
  })

  // Роутер, который ещё ни разу не выходил на связь, -- не авария и не норма.
  it('роутер ни разу не отвечал -- честное неизвестно', () => {
    const h = routerHeadline({
      router: { status: 'offline', nickname: 'new', last_seen_age_sec: null },
      traffic: null,
      incidents: [],
    })
    expect(h.tone).toBe('off')
    expect(h.tag).toContain('ещё ни разу')
  })

  // stale -- это не то же самое, что cold. Холодная шапка бывает и у живого
  // роутера с тревогой; устаревшими показания становятся только тогда, когда
  // роутер молчит, и тогда их нельзя показывать как текущие НИГДЕ на экране.
  it('молчащий роутер помечает все показания устаревшими', () => {
    const h = routerHeadline({
      router: { status: 'offline', last_seen_age_sec: 900 },
      traffic: { mode: 'vpn', egress_tunnel_name: 'awg11' },
      incidents: [],
    })
    expect(h.stale).toBe(true)
  })

  it('живой роутер с тревогой -- шапка холодная, но показания свежие', () => {
    const h = routerHeadline({
      router: ONLINE,
      traffic: { mode: 'vpn', egress_tunnel_name: 'awg11' },
      incidents: [{ check_name: 'hydraroute' }],
    })
    expect(h.cold).toBe(true)
    expect(h.stale).toBe(false)
  })

  it('нет данных о трафике -- не выдумываем вердикт', () => {
    const h = routerHeadline({ router: ONLINE, traffic: null, incidents: [] })
    expect(h.tone).toBe('off')
    expect(h.verdict).toContain('не сообщил')
  })
})
