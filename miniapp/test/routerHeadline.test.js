import { describe, it, expect } from 'vitest'
import { routerHeadline, linesSummary } from '../src/routerHeadline.js'

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
    // Метка отвечает на вопрос человека («всё ли хорошо»), а не пересказывает
    // прибор. Возраст данных ушёл в отдельную строку свежести, которая тикает
    // сама: держать его в метке значило бы печатать число, застывшее на
    // момент загрузки.
    expect(h.tag).toBe('всё работает')
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

// Строка под корпусом роутера считала VPN-туннели в две формы -- «1 VPN-туннель»
// и всё остальное «VPN-туннеля». На нуле выходило «0 VPN-туннеля из 2»: в приложении, где
// человек сверяет показания с роутером, сломанное склонение читается как
// сломанные данные.
describe('linesSummary', () => {
  it('склоняет VPN-туннели по-русски', () => {
    expect(linesSummary(0, 2)).toBe('0 VPN-туннелей из 2')
    expect(linesSummary(1, 2)).toBe('1 VPN-туннель из 2')
    expect(linesSummary(2, 3)).toBe('2 VPN-туннеля из 3')
    expect(linesSummary(5, 7)).toBe('5 VPN-туннелей из 7')
    expect(linesSummary(21, 30)).toBe('21 VPN-туннель из 30')
  })

  it('без туннелей говорит прямо, а не «0 из 0»', () => {
    expect(linesSummary(0, 0)).toBe('VPN-туннелей нет')
  })
})

describe('вердикт говорит правду о маршруте', () => {
  // Главная ложь старого экрана: обещание единого ответа «через VPN или
  // напрямую». Маршрут всегда умный -- заблокированное идёт через туннель,
  // остальное напрямую, — и вердикт обязан говорить про ДВА потока.
  it('не обещает единого маршрута для всего трафика', () => {
    const h = routerHeadline({
      router: { status: 'online', last_seen_age_sec: 6 },
      traffic: { mode: 'vpn', egress_tunnel_name: 'Амстердам' },
      incidents: [],
    })
    expect(h.verdict).not.toMatch(/весь трафик|весь интернет/i)
    expect(h.verdict).toMatch(/заблокирован/i)
    expect(h.verdict).toMatch(/напрямую/i)
    expect(h.tag).toBe('всё работает')
  })

  it('на упавшей линии называет последствие человеческими словами', () => {
    const h = routerHeadline({
      router: { status: 'online', last_seen_age_sec: 6 },
      traffic: { mode: 'vpn' },
      incidents: [{ check_name: 'tunnel_awg12' }],
    })
    expect(h.tone).toBe('danger')
    expect(h.tag).not.toMatch(/tunnel_|awg\d/)
  })
})

describe('упавшая линия при живом резерве', () => {
  // Пугать «заблокированное не открывается», когда вторая линия работает и
  // обход через неё идёт, — это ложная тревога. Человек побежит чинить то,
  // что у него работает.
  it('не объявляет обход сломанным, пока жива другая линия', () => {
    const h = routerHeadline({
      router: { status: 'online', last_seen_age_sec: 6 },
      traffic: { mode: 'singbox' },
      incidents: [{ check_name: 'tunnel_awg12' }],
      tunnels: [
        { tunnel_id: 'awg12', name: 'Амстердам', run_state: 'stopped' },
        { tunnel_id: 'awg10', name: 'Франкфурт', run_state: 'running' },
      ],
    })
    expect(h.tag).not.toMatch(/не открывается/)
    expect(h.verdict).toMatch(/Франкфурт|запасн|другую/i)
  })

  // А вот когда живых линий не осталось -- это именно то, чем кажется.
  it('без единой живой линии говорит прямо', () => {
    const h = routerHeadline({
      router: { status: 'online', last_seen_age_sec: 6 },
      traffic: { mode: 'vpn' },
      incidents: [{ check_name: 'tunnel_awg12' }],
      tunnels: [{ tunnel_id: 'awg12', name: 'Амстердам', run_state: 'stopped' }],
    })
    expect(h.tag).toBe('заблокированное не открывается')
  })
})
