// Самопроверка скрипта раскладки: на каждую из 8 проверок -- одна НАРУШАЮЩАЯ
// страница, на которой проверка обязана сработать, и одна чистая, на которой
// не срабатывает ничего. Так видно, что проверка вообще умеет стрелять.
// Запуск: node miniapp/layout/selftest.mjs  (код выхода 1 при любом провале).
import { chromium } from 'playwright'
import { collectLayout, findProblems, netProblems, SMALL_OK, SKIP_TARGETS } from './checks.js'
import { watchNet } from './net.mjs'

const base = '<style>body{margin:0;font:14px sans-serif} button,select{font:inherit;padding:0;border:0;background:#223}</style>'
const btn = (t, extra = '') => `<button style="min-width:48px;height:48px;${extra}">${t}</button>`
const CLEAN = `${base}<div style="padding:8px"><button class="btn-primary" style="width:120px;height:48px">Главная</button>
  <div style="display:flex;gap:8px;margin-top:8px">${btn('Раз')}${btn('Два')}</div>
  <div style="width:200px;overflow:hidden;white-space:nowrap"><span>короткий</span></div>
  <label style="display:inline-block;min-width:48px;min-height:48px"><input type="checkbox"> тумблер</label>
  <div style="width:300px;overflow:hidden"><nav style="display:flex;gap:8px;overflow-x:auto;white-space:nowrap">${['router4car4new', 'дача-северная', 'sandbox-broken', 'четвёртый-роутер'].map((n) => `<button style="flex:none;height:44px;padding:0 12px"><span>${n}</span></button>`).join('')}</nav></div></div>`

const FIXTURES = [
  [1, 'страница шире окна', `${base}<div style="width:900px;height:20px;background:#ccc">широкая</div>`],
  [2, 'цель 20x20', `${base}${btn('мелкая', 'width:20px;height:20px;min-width:0')}`],
  [2, 'переключатель-label 22 px', `${base}<label style="display:inline-block;height:22px"><input type="checkbox" style="opacity:0;position:absolute"> тумблер</label>`],
  [2, 'select 24 px', `${base}<select style="height:24px"><option>а</option></select>`],
  [3, 'две лаймовые', `${base}<button class="btn-primary" style="height:48px;width:100px">Раз</button><button class="btn-primary" style="height:48px;width:100px">Два</button>`],
  [4, 'текст 10 px', `${base}<p style="font-size:10px">мелкий смысловой текст</p>`],
  [5, 'свой текст шире коробки', `${base}<div style="width:60px;overflow:hidden;white-space:nowrap">очень длинная строка без переноса</div>`],
  [5, 'строчный текст в обрезающем предке', `${base}<div style="width:60px;overflow:hidden;white-space:nowrap"><span>очень длинная строка без переноса</span></div>`],
  [5, 'предок без своего текста', `${base}<div style="width:60px;overflow:hidden"><div><div style="width:200px">широкий блок с текстом</div></div></div>`],
  [6, 'ряд кнопок разной высоты (прямые соседи)', `${base}<div style="display:flex;align-items:flex-start;gap:8px"><button style="height:64px;width:90px">Высокая</button><button style="height:44px;width:90px">Низкая</button></div>`],
  [6, 'ряд через display:contents', `${base}<div style="display:flex;align-items:flex-start;gap:8px"><div style="display:contents"><button style="height:64px;width:90px">Высокая</button></div><button style="height:44px;width:90px">Низкая</button></div>`],
  [6, 'разная высота при смещённом верхе (центр)', `${base}<div style="display:flex;align-items:center;gap:8px"><button style="height:64px;width:90px">Высокая</button><a class="btn" href="#x" style="display:inline-block;height:44px;width:90px">Ссылка</a></div>`],
  [8, 'кнопка в оформлении браузера', '<style>body{margin:0;font:14px sans-serif}</style><button style="min-width:120px;height:48px">голая кнопка</button>'],
  [8, 'поле ввода в оформлении браузера', '<style>body{margin:0;font:14px sans-serif}</style><input style="width:200px;height:48px" value="поле">'],
  [8, 'кнопка без фона, но со шрифтом браузера', '<style>body{margin:0;font:14px sans-serif}</style><button style="min-width:120px;height:48px;background:#223;border:0">чужой шрифт</button>'],
]

let failed = 0
const report = (ok, msg) => {
  console.log(`${ok ? 'OK  ' : 'FAIL'} ${msg}`)
  if (!ok) failed++
}

const browser = await chromium.launch()
try {
  const page = await (await browser.newContext({ viewport: { width: 360, height: 640 } })).newPage()
  const measure = async (html) => {
    await page.setContent(html)
    return findProblems(await page.evaluate(collectLayout, { smallOk: SMALL_OK, skip: SKIP_TARGETS }))
  }
  for (const [check, name, html] of FIXTURES) {
    const got = (await measure(html)).map((p) => p.check)
    report(got.includes(check), `проверка ${check} срабатывает: ${name} (получено [${got}])`)
  }
  const clean = await measure(CLEAN)
  report(clean.length === 0, `чистая страница -- без находок (${clean.map((p) => `[${p.check}] ${p.what}`).join('; ') || 'пусто'})`)

  // Исключение мелкого текста: машинный код не должен срабатывать.
  const code = await measure(`${base}<span class="data-row-code" style="font-size:11px">check_direct</span>`)
  report(code.length === 0, 'машинный код 11 px -- исключение, находок нет')

  // 7: настоящие слушатели -- консоль и ответ 500 через маршрут Playwright.
  const events = []
  watchNet(page, events)
  await page.route('http://example.test/**', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"code":"boom"}' }))
  await page.setContent(`${base}<p>сеть</p>`)
  await page.evaluate(() => {
    console.error('нарушитель консоли')
    return fetch('http://example.test/v1/x', { method: 'POST', body: '{"action":"a"}' }).catch(() => null)
  })
  await page.waitForTimeout(500)
  const net = netProblems(events)
  report(net.length >= 2 && net.every((p) => p.check === 7), `проверка 7 срабатывает на консоли и ответе 500 (событий ${events.length}, находок ${net.length})`)
  const quiet = []
  report(netProblems(quiet).length === 0, 'проверка 7: тишина -- без находок')
} finally {
  await browser.close()
}
console.log(failed ? `САМОПРОВЕРКА ПРОВАЛЕНА: ${failed}` : 'самопроверка пройдена')
process.exit(failed ? 1 : 0)
