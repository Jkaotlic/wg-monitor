// «Куда пойдёт сайт»: чистые функции раздела на экране маршрутов. Агент
// отвечает на route_lookup фактами и кодами (wire.RouteLookupResult), слова
// для владельца подбираются здесь -- о последствиях, без внутренней кухни.

const NOTE_TEXT = {
  hr_not_running: 'HydraRoute Neo — движок умной раздельной маршрутизации — сейчас не запущен, его правила не действуют',
  ip_rules_unchecked: 'Есть правила по адресу — по имени сайта их не проверить',
  regexp_unchecked: 'Часть правил записана шаблоном — их не проверить',
  policies_unknown: 'Роутер не отдал общие наборы правил',
  singbox_router: 'Трафиком управляет sing-box — он решает сам',
}
const GEO_EXPAND_FAILED = 'geo_expand_failed:'
const EXIT_UNRECOGNIZED = 'exit_unrecognized:'

// NOT_A_SITE -- одни и те же слова для имени, отбитого проверкой на экране,
// сервером (400 invalid_domain) и агентом.
export const NOT_A_SITE = 'Это не похоже на адрес сайта — нужно имя вроде claude.ai'

// normalizeSiteInput оставляет от того, что вставил человек, одно имя сайта:
// без схемы, пути, запроса, якоря, порта и хвостовой точки, в нижнем
// регистре. www. не трогается -- это другое имя, и правила бывают на нём.
export function normalizeSiteInput(raw) {
  let s = String(raw ?? '').trim().toLowerCase()
  s = s.replace(/^https?:\/\//, '')
  s = s.split(/[/?#]/, 1)[0]
  s = s.replace(/:\d*$/, '')
  return s.replace(/\.+$/, '').trim()
}

// looksLikeSite -- та же проверка, что у сервера (sanitizeWizardCommandArgs,
// route_lookup): не пусто, не длиннее 253, есть точка, нет пробелов, «/» и
// «:». Отбитое сервером не отправляется, а человек сразу слышит, что не так.
// Решает всё равно сервер: это подсказка, а не граница.
export function looksLikeSite(domain) {
  const d = String(domain ?? '')
  return d.length > 0 && d.length <= 253 && d.includes('.') && !/[\s/:]/.test(d)
}

function noteText(code) {
  if (typeof code !== 'string') return ''
  if (code.startsWith(GEO_EXPAND_FAILED)) {
    return `Роутер не раскрыл список «${code.slice(GEO_EXPAND_FAILED.length)}»`
  }
  if (code.startsWith(EXIT_UNRECOGNIZED)) {
    return `Сайт уйдёт через подключение «${code.slice(EXIT_UNRECOGNIZED.length)}» — роутер не сказал, VPN-туннель это или провайдер`
  }
  return NOTE_TEXT[code] ?? ''
}

function parseLookup(result) {
  if (typeof result === 'string') {
    try {
      return parseLookup(JSON.parse(result))
    } catch {
      return null
    }
  }
  return result && typeof result === 'object' ? result : null
}

function destination(via, tunnelName) {
  if (via === 'tunnel') return `через VPN-туннель «${tunnelName}»`
  if (via === 'direct') return 'напрямую через провайдера'
  return 'неизвестно куда'
}

// lookupAnswer turns a route_lookup result (object or the command's JSON
// output) into { title, lines, tone }. tone: ok -- ответ есть; warn -- ответ
// есть, но в нём есть подвох; unknown -- ответа нет.
export function lookupAnswer(result) {
  const r = parseLookup(result)
  if (!r) {
    return {
      title: 'Не удалось узнать, куда пойдёт сайт',
      lines: ['Роутер прислал ответ, который не удалось разобрать'],
      tone: 'unknown',
    }
  }
  const site = `«${r.domain || 'сайт'}»`
  const tunnel = r.tunnel_name || r.tunnel_id
  const matches = Array.isArray(r.matches) ? r.matches : []
  const codes = Array.isArray(r.notes) ? r.notes : []
  const notes = [...new Set(codes.map(noteText).filter(Boolean))]
  const rules = [...new Set(matches.map((m) => `правило «${m.rule_name}»`))]
  // Остановленный движок значит, что правила, на которые человек, возможно,
  // рассчитывает, не действуют: ответ верный, но повод насторожиться.
  const tone = codes.includes('hr_not_running') ? 'warn' : 'ok'

  switch (r.verdict) {
    case 'tunnel':
      return {
        title: r.by_default
          ? `Правил для ${site} нет — сайт пойдёт главным выходом роутера, через VPN-туннель «${tunnel}»`
          : `${site} пойдёт через VPN-туннель «${tunnel}»`,
        lines: [...rules, ...notes],
        tone,
      }
    case 'direct':
      return {
        title: r.by_default
          ? `Правил для ${site} нет — сайт пойдёт напрямую через провайдера`
          : `${site} пойдёт напрямую через провайдера`,
        lines: [...rules, ...notes],
        tone,
      }
    case 'mixed':
      return {
        title: `Несколько правил ведут ${site} в разные места`,
        lines: [
          ...matches.map((m) => `правило «${m.rule_name}» — ${destination(m.via, m.tunnel_name || m.tunnel_id)}`),
          ...notes,
        ],
        tone: 'warn',
      }
    default: {
      // Причина обязана прозвучать: «неизвестно» без «почему» человеку
      // ничего не даёт.
      const why = notes.length
        ? notes
        : [r.by_default ? 'Роутер не назвал свой главный выход' : 'Роутер не сказал, куда ведёт это правило']
      return { title: `Не удалось узнать, куда пойдёт ${site}`, lines: [...rules, ...why], tone: 'unknown' }
    }
  }
}

// lookupRefusal -- слова для отказа: агент и сервер отвечают по-английски и
// для людей не пишут. code -- код ошибки сервера (useCommand errorCode):
// текст ошибки у api.js кода не несёт, и по одному тексту отказ сервера в
// имени читался бы как «роутер не ответил».
export function lookupRefusal(output, code) {
  if (code === 'invalid_domain' || /invalid[ _]domain/i.test(String(output ?? ''))) return NOT_A_SITE
  return 'Роутер не ответил на вопрос — попробуйте ещё раз'
}
