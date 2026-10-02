// Скрипт раскладки (v0.52, спека §9): на каждую роль и каждую ширину поднимает
// СВОЮ песочницу (порты 8121-8126; тревога песочницы стареет за 5 минут, так что
// «Починить» каждый раз видит свежую), обходит SCREENS, снимает каждый экран и
// падает при любой находке. В CI не входит, перед выпуском обязателен.
// Каждую свою песочницу гасит сам -- и при обычном выходе, и по SIGINT/SIGTERM/
// SIGHUP, и при исключении.
import { spawn, execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, appendFileSync, openSync, closeSync } from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { collectLayout, findProblems, netProblems, SMALL_OK, SKIP_TARGETS } from './checks.js'
import { watchNet } from './net.mjs'
import { killChild, stopChild } from './proc.mjs'
import { ROLES, WIDTHS, SCREENS, DEFAULT_ROUTER, SANDBOX_LATEST, HRNEO_STOPPED, expectPattern } from './screens.js'

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const args = Object.fromEntries(process.argv.slice(2).map((a, i, all) => (a.startsWith('--') ? [a.slice(2), all[i + 1]] : null)).filter(Boolean))
const roles = args.roles ? args.roles.split(',') : ROLES
const widths = args.widths ? args.widths.split(',').map(Number) : WIDTHS
// --screens a,b -- только эти экраны (отладка одного места; приёмка -- без него).
const only = args.screens ? args.screens.split(',') : null
const out = args.out ?? path.join(os.tmpdir(), `wgm-layout-${new Date().toISOString().replace(/[:.]/g, '-')}`)
mkdirSync(out, { recursive: true })

// ---- песочницы: ни одна не переживает скрипт -------------------------------
const children = new Set()
function killAll() {
  for (const c of children) killChild(c)
  children.clear()
}
process.on('exit', killAll)
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    killAll()
    flush()
    process.exit(130)
  })
}

function buildSandbox() {
  if (args.bin) return args.bin
  const bin = path.join(out, 'sandbox')
  const env = { ...process.env, PATH: `${os.homedir()}/sdk/go/bin:${process.env.PATH}` }
  execFileSync('go', ['build', '-o', bin, './cmd/miniapp-sandbox'], { cwd: REPO, env, stdio: 'inherit' })
  return bin
}

function portBusy(port) {
  return new Promise((resolve) => {
    const s = net.connect(port, '127.0.0.1')
    s.once('connect', () => { s.destroy(); resolve(true) })
    s.once('error', () => resolve(false))
  })
}

async function waitHTTP(url, ms) {
  const until = Date.now() + ms
  while (Date.now() < until) {
    try {
      if ((await fetch(url)).ok) return
    } catch {
      // ещё поднимается
    }
    await new Promise((r) => setTimeout(r, 300))
  }
  throw new Error(`песочница не ответила: ${url}`)
}

// ---- отчёт: пишется по ходу, исключение не теряет сделанное -----------------
const report = []
function flush() {
  try {
    writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2))
    const bad = report.filter((e) => e.problems.length)
    const lines = bad.flatMap((e) => e.problems.map((p) => `${e.role}\t${e.width}\t${e.screen}\t[${p.check}] ${p.what}${e.router ? `\t(роутер ${e.router})` : ''}`))
    writeFileSync(path.join(out, 'report.txt'), lines.join('\n') + '\n')
  } catch {
    // отчёт -- не повод падать второй раз
  }
}
function record(entry) {
  report.push(entry)
  try {
    appendFileSync(path.join(out, 'report.jsonl'), JSON.stringify(entry) + '\n')
  } catch {
    // см. flush
  }
}

// ---- действия над страницей -----------------------------------------------
// Дефис в тексте шага -- как обычный, так и неразрывный (text.js).
const hy = (s) => s.replace(/-/g, '[-‑]')

async function clickText(page, text) {
  const handle = await page.evaluateHandle((t) => {
    // Интерфейс печатает «VPN‑туннель» с неразрывным дефисом (text.js) -- сравниваем без него.
    const norm = (s) => (s || '').trim().replace(/\s+/g, ' ').replace(/[‐‑]/g, '-')
    const sheet = document.querySelector('.sheet')
    const root = sheet ?? document
    const els = [...root.querySelectorAll('button, a[href], [role=tab], summary')].filter((e) => e.offsetParent !== null || getComputedStyle(e).position === 'fixed')
    return els.find((e) => norm(e.innerText) === t) ?? els.find((e) => norm(e.innerText).startsWith(t)) ?? null
  }, text)
  const el = handle.asElement()
  if (!el) return false
  await el.click({ timeout: 5000 })
  await page.waitForTimeout(700)
  return true
}

async function reset(page) {
  for (let i = 0; i < 6; i++) {
    if (await page.$('.sheet')) {
      if (!(await clickText(page, 'Отмена'))) await page.keyboard.press('Escape')
      continue
    }
    const back = await page.$('#sandbox-back')
    if (back && (await back.isVisible())) { await back.click(); await page.waitForTimeout(400); continue }
    const overlayBack = await page.$('.overlay-back:not([disabled])')
    if (overlayBack && (await overlayBack.isVisible())) { await overlayBack.click(); await page.waitForTimeout(400); continue }
    break
  }
}

const currentRouter = (page) =>
  page.evaluate(() => {
    const e = document.querySelector('.router-switch-name, .app-header-title, .main-head-name')
    return e ? e.textContent.trim() : null
  })

// Выбор роутера по точному имени; результат проверяется по шапке, а не по клику.
async function pickRouter(page, name) {
  if ((await currentRouter(page)) === name) return true
  const clickByName = (listSel, nameSel) =>
    page.evaluate(([l, n, want]) => {
      const row = [...document.querySelectorAll(l)].find((r) => r.querySelector(n)?.textContent.trim() === want)
      if (!row) return false
      row.click()
      return true
    }, [listSel, nameSel, name])
  let done = (await clickByName('.side-row', '.side-row-name')) || (await clickByName('.strip-chip', '.strip-name'))
  if (!done && (await page.$('.router-switch'))) {
    await (await page.$('.router-switch')).click()
    await page.waitForTimeout(500)
    done = await clickByName('.fleet-row', '.row-title')
  }
  if (!done) return false
  await page.waitForTimeout(800)
  return (await currentRouter(page)) === name
}

async function runStep(page, step, routerName, role) {
  if (step.tab) {
    if (await clickText(page, step.tab)) return true
    // Админ без роутера уже на Парке, панели вкладок нет (одна вкладка).
    return step.tab === 'Парк' && Boolean(await page.$('.park-tab'))
  }
  if (step.click) return clickText(page, step.click)
  if (step.segment) return clickText(page, step.segment)
  if (step.clickAll) {
    const n = await page.evaluate((t) => {
      const norm = (s) => (s || '').trim().replace(/\s+/g, ' ')
      const bs = [...document.querySelectorAll('button')].filter((b) => b.offsetParent !== null && norm(b.innerText).startsWith(t))
      bs.forEach((b) => b.click())
      return bs.length
    }, step.clickAll)
    await page.waitForTimeout(500)
    // Уже раскрыто прежним экраном (состояние карточек живёт между экранами): «Скрыть ▾».
    return n > 0 || (await page.evaluate(() => [...document.querySelectorAll('button')].some((b) => b.offsetParent !== null && b.innerText.trim().startsWith('Скрыть'))))
  }
  if (step.headerPick) {
    // На широкой раскладке список роутеров -- колонка и всегда на экране: он
    // должен быть настоящим (строки есть). На узкой -- открыть выбор из шапки.
    if (await page.$('.wide-shell')) return (await page.$$('.side-row')).length > 0
    const sw = await page.$('.router-switch')
    if (!sw) return false
    await sw.click()
    await page.waitForTimeout(600)
    return (await page.$$('.fleet-row')).length > 0
  }
  if (step.stripScroll) {
    // Прокрутка рукой: красный чип -- вне прокрутки, двигаются остальные.
    const ok = await page.evaluate((to) => {
      const el = document.querySelector('.router-strip .strip-scroll') ?? document.querySelector('.router-strip')
      if (!el) return false
      el.scrollLeft = to === 'end' ? el.scrollWidth : Number(to)
      return true
    }, step.stripScroll)
    await page.waitForTimeout(300)
    return ok
  }
  if (step.stripPick) {
    for (const c of await page.$$('.router-strip .strip-chip')) {
      if ((await c.getAttribute('aria-label') ?? '').startsWith(step.stripPick)) {
        // Настоящее нажатие: Playwright сам докручивает чип в кадр и убеждается,
        // что нажатие получает он, а не то, что лежит поверх.
        await c.click({ timeout: 5000 })
        await page.waitForTimeout(900)
        return true
      }
    }
    return false
  }
  if (step.stripExpect) {
    // Геометрия полосы: красный чип стоит у левого поля и НЕ пересекается ни с
    // одним другим чипом (видимые части -- по краю своей прокрутки), где бы ни
    // стояла прокрутка. 'effect' -- перемотку сделало приложение: текущий чип
    // начинается в кадре. Числа -- в журнал прогона.
    const g = await page.evaluate(() => {
      const strip = document.querySelector('.router-strip')
      const red = strip?.querySelector('.strip-chip-alert')
      const cur = strip?.querySelector('.strip-chip-current')
      if (!strip || !red || !cur) return null
      const scroll = strip.querySelector('.strip-scroll') ?? strip
      const sr = strip.getBoundingClientRect()
      const box = scroll.getBoundingClientRect()
      const rr = red.getBoundingClientRect()
      const cr = cur.getBoundingClientRect()
      const others = [...strip.querySelectorAll('.strip-chip')].filter((c) => c !== red).map((c) => {
        const r = c.getBoundingClientRect()
        const inScroll = scroll !== strip && scroll.contains(c)
        return { name: c.textContent.trim(), left: inScroll ? Math.max(r.left, box.left) : r.left, right: inScroll ? Math.min(r.right, box.right) : r.right }
      }).filter((o) => o.right > o.left)
      return {
        stripLeft: sr.left, stripRight: sr.right, scrollBoxLeft: box.left,
        redLeft: rr.left, redRight: rr.right, curLeft: cr.left, curRight: cr.right,
        same: red === cur, scrollLeft: scroll.scrollLeft, scrollMax: scroll.scrollWidth - scroll.clientWidth, others,
      }
    })
    if (!g) return false
    console.log(`  [полоса ${step.stripExpect} ${role} ${page.viewportSize().width}] ` + JSON.stringify(g))
    if (Math.abs(g.redLeft - g.stripLeft - 16) > 0.5) return false
    if (g.others.some((o) => Math.min(o.right, g.redRight) - Math.max(o.left, g.redLeft) > 0.5)) return false
    if (step.stripExpect === 'pinned') return g.scrollMax > 0 && Math.abs(g.scrollLeft - g.scrollMax) <= 1
    if (step.stripExpect === 'effect' && !g.same) return g.curLeft >= g.redRight && g.curLeft >= g.scrollBoxLeft - 0.5 && g.curLeft < g.stripRight
    return true
  }
  if (step.sheetChoice) {
    for (const c of await page.$$('.sheet-choice')) {
      if ((await c.innerText()).trim().replace(/[‐‑]/g, '-').startsWith(step.sheetChoice)) {
        await c.click()
        await page.waitForTimeout(700)
        return true
      }
    }
    return false
  }
  if (step.expandAll) {
    for (const s of await page.$$('details.manage-group:not([open]) > summary')) await s.click()
    await page.waitForTimeout(400)
    return true
  }
  if (step.first) { const el = await page.$(step.first); if (!el) return false; await el.click(); await page.waitForTimeout(700); return true }
  if (step.fill) {
    const input = await page.$('.sheet input:not([type=checkbox]):not([type=radio])')
    if (!input) return false
    await input.fill(step.fill.replace('$router', routerName ?? ''))
    await page.waitForTimeout(300)
    return true
  }
  if (step.expect) {
    // Только верхний слой (лист, иначе верхний оверлей, иначе страница): у
    // body.innerText есть и слой под ним -- фраза оттуда проходила бы вхолостую.
    const text = await page.evaluate(() => {
      const top = document.querySelector('.sheet') ?? [...document.querySelectorAll('.overlay, .deploy-wait')].pop() ?? document.body
      return top.innerText.replace(/[‐‑]/g, '-')
    })
    // innerText отдаёт текст после text-transform (заголовки капсом): без учёта регистра.
    return new RegExp(expectPattern(step, role), 'i').test(text)
  }
  if (step.fillSel) {
    // Поле листа по селектору. '$confirm' -- фраза, которую лист просит набрать
    // (она на экране, в подписи поля): скрипт набирает то, что набрал бы человек.
    const input = await page.$(`.sheet ${step.fillSel}`)
    if (!input) return false
    let value = step.value
    if (value === '$confirm') {
      value = await page.evaluate(() => document.querySelector('.sheet label[for=sheet-confirm-input] .q')?.textContent ?? null)
      if (!value) return false
      value = value.replace(/^«|»$/g, '').replace(/[‐‑]/g, '-')
    }
    await input.fill(value)
    await page.waitForTimeout(300)
    return true
  }
  if (step.waitText || step.waitButton) {
    // Дождаться слов (или кнопки) в верхнем слое: задание песочницы идёт секунды.
    const until = Date.now() + (step.ms ?? 15000)
    while (Date.now() < until) {
      const found = await page.evaluate(([text, button]) => {
        const norm = (s) => (s || '').trim().replace(/\s+/g, ' ').replace(/[‐‑]/g, '-')
        const top = document.querySelector('.sheet') ?? [...document.querySelectorAll('.overlay, .deploy-wait')].pop() ?? document.body
        if (button) return [...top.querySelectorAll('button')].some((b) => b.offsetParent !== null && !b.disabled && norm(b.innerText) === button)
        return norm(top.innerText).toLowerCase().includes(text.toLowerCase())
      }, [step.waitText ?? null, step.waitButton ?? null])
      if (found) return true
      await page.waitForTimeout(300)
    }
    return false
  }
  if (step.rowIn) {
    const row = await page.$(`.section:has(.section-title:text-matches("^${hy(step.rowIn)}")) .list-row-btn`)
    if (!row) return false
    await row.click()
    await page.waitForTimeout(700)
    return true
  }
  if (step.tunnelWith) {
    const list = '.section:has(.section-title:text-matches("^Все VPN[-‑]туннели")) .list-row-btn'
    const n = (await page.$$(list)).length
    for (let i = 0; i < n; i++) {
      const rows = await page.$$(list)
      await rows[i].click()
      await page.waitForTimeout(700)
      if (await page.$(`.overlay button:text-matches("^${hy(step.tunnelWith)}$")`)) return true
      await reset(page)
    }
    return false
  }
  throw new Error(`неизвестный шаг ${JSON.stringify(step)}`)
}

// Раскрыть всё, что человек раскрывает: details/summary и «История за 24ч» --
// в видимом слое (лист, иначе верхний слой, иначе страница).
async function expandEverything(page) {
  for (let pass = 0; pass < 3; pass++) {
    const n = await page.evaluate(() => {
      const root = document.querySelector('.sheet') ?? [...document.querySelectorAll('.overlay, .deploy-wait')].pop() ?? document.body
      let c = 0
      for (const d of root.querySelectorAll('details:not([open])')) { d.querySelector(':scope > summary')?.click(); c++ }
      for (const b of root.querySelectorAll('button')) {
        if (b.offsetParent !== null && b.innerText.trim() === 'История за 24ч') { b.click(); c++ }
      }
      return c
    })
    if (!n) break
    await page.waitForTimeout(500)
  }
}

// Снимок всего экрана -- высоким окном, а не fullPage: при fullPage Chromium на
// время съёмки подменяет окно, медиа-правила широкой раскладки отпадают, и
// колонка попадала в кадр посреди перехода в оформлении браузера (серые
// кнопки, которых человек не видит); закреплённая панель вкладок вставала
// посреди кадра, а прокрутка внутри слоя и колонки обрезалась. Высокое окно
// даёт то, что человек увидел бы, пролистав до конца. Замеры уже сняты до
// этого, в настоящем окне.
async function shoot(page, file, width) {
  const base = page.viewportSize()
  const need = await page.evaluate(() => {
    // Лист или слой во весь экран (телефон) закрывают страницу: высота -- по
    // ним, а не по странице под ними (иначе кадр тянулся на её длину пустотой).
    const fixed = (el) => el && getComputedStyle(el).position === 'fixed'
    const sheet = document.querySelector('.sheet')
    const overlay = [...document.querySelectorAll('.overlay, .deploy-wait')].pop()
    const root = sheet ? null : fixed(overlay) ? overlay : null
    let h = root ? window.innerHeight : document.scrollingElement.scrollHeight
    for (const el of (root ?? document.body).querySelectorAll('*')) {
      if (el.scrollHeight <= el.clientHeight + 1 || !el.getClientRects().length) continue
      if (/(auto|scroll)/.test(getComputedStyle(el).overflowY)) h = Math.max(h, window.innerHeight + el.scrollHeight - el.clientHeight)
    }
    return h
  })
  const tall = need > base.height
  if (tall) {
    await page.setViewportSize({ width, height: Math.min(need, 8000) })
    await page.waitForTimeout(350)
  }
  await page.screenshot({ path: file })
  if (tall) {
    await page.setViewportSize(base)
    await page.waitForTimeout(150)
  }
}

// ---- один проход: роль × ширина ------------------------------------------
async function runPass(bin, role, width, port) {
  const dir = path.join(out, role, String(width))
  mkdirSync(path.join(dir, 'tmp'), { recursive: true })
  const logFd = openSync(path.join(dir, 'sandbox.log'), 'w')
  // TMPDIR песочницы -- под выводом прогона: её временные каталоги не утекают.
  // -latest: Парк без похода на GitHub (одна и та же «доступная версия» на
  // каждом прогоне); -backend-update ignore: раскатка бэкенда остаётся в
  // ожидании -- экран снимается в устойчивом состоянии, а не за секунду до
  // перезагрузки страницы; -hrneo-stopped: роутер с остановленным HydraRoute
  // Neo (по умолчанию песочница никого не останавливает).
  const child = spawn(bin, ['-addr', `127.0.0.1:${port}`, '-role', role, '-db', path.join(dir, 'sandbox.db'), '-latest', SANDBOX_LATEST, '-backend-update', 'ignore', '-hrneo-stopped', HRNEO_STOPPED], {
    cwd: REPO,
    detached: true,
    stdio: ['ignore', logFd, logFd],
    env: { ...process.env, TMPDIR: path.join(dir, 'tmp') },
  })
  closeSync(logFd)
  children.add(child)
  let browser
  try {
    await waitHTTP(`http://127.0.0.1:${port}/miniapp/`, 60000)
    browser = await chromium.launch()
    const page = await (await browser.newContext({ viewport: { width, height: width < 1024 ? 800 : 900 } })).newPage()
    const events = []
    watchNet(page, events)

    // Вход -- один на проход: у песочницы лимит входов (10, затем 1 в 5 с).
    const boot = { role, width, screen: 'boot', problems: [] }
    try {
      await page.goto(`http://127.0.0.1:${port}/miniapp/`)
      // .screen без оболочки -- экран «Роутер ещё не привязан» (роль none).
      await page.waitForSelector('.app-header, .wide-shell, .screen', { timeout: 15000 })
      await page.waitForTimeout(1000)
    } catch (e) {
      boot.problems.push({ check: 0, what: `первая загрузка не дошла до оболочки: ${e.message.split('\n')[0]}` })
    }
    boot.problems.push(...netProblems(events))
    record(boot)
    if (boot.problems.some((p) => p.check === 0)) return

    for (const screen of SCREENS.filter((s) => s.roles.includes(role) && (s.maxWidth == null || width <= s.maxWidth) && (!only || only.includes(s.id)))) {
      await reset(page)
      events.length = 0
      const entry = { role, width, screen: screen.id, router: null, problems: [] }
      const want = screen.router ? (screen.routers?.[role] ?? (role === 'admin' ? screen.router : DEFAULT_ROUTER[role])) : null
      try {
        let opened = true
        if (want) {
          opened = await pickRouter(page, want)
          if (!opened) entry.problems.push({ check: 0, what: `роутер «${want}» не выбран: на экране «${await currentRouter(page)}»` })
        }
        for (const step of screen.steps) {
          if (!opened) break
          opened = await runStep(page, step, want, role)
          if (!opened) {
            if (!entry.problems.length) entry.problems.push({ check: 0, what: `шаг обхода не удался: ${JSON.stringify(step)}` })
          }
        }
        if (want) {
          entry.router = await currentRouter(page)
          if (opened && entry.router !== want) entry.problems.push({ check: 0, what: `снят не тот роутер: нужен «${want}», на экране «${entry.router}»` })
        }
        // Пропусков нет: экран, который не открылся, -- находка (шаг 0).
        if (opened) {
          await expandEverything(page)
          await page.waitForTimeout(600)
          const data = await page.evaluate(collectLayout, { smallOk: SMALL_OK, skip: SKIP_TARGETS })
          entry.problems.push(...findProblems(data), ...netProblems(events))
          mkdirSync(dir, { recursive: true })
          await shoot(page, path.join(dir, `${screen.id}.png`), width)
        }
        // Закреплённый экран (ожидание раскатки бэкенда) «назад» не отпускает:
        // дальше -- с чистой страницы.
        if (screen.reload) {
          await page.goto(`http://127.0.0.1:${port}/miniapp/`)
          await page.waitForSelector('.app-header, .wide-shell, .screen', { timeout: 15000 })
          await page.waitForTimeout(800)
        }
      } catch (e) {
        entry.problems.push({ check: 0, what: `исключение обхода: ${e.message.split('\n')[0]}` })
      }
      record(entry)
    }
  } finally {
    if (browser) await browser.close().catch(() => {})
    // Ждём выхода песочницы: следующий проход берёт тот же порт.
    await stopChild(child)
    children.delete(child)
    flush()
  }
}

try {
  const bin = buildSandbox()
  for (const [i, role] of roles.entries()) {
    const port = 8121 + i
    for (const width of widths) {
      if (await portBusy(port)) throw new Error(`порт ${port} занят чужим процессом -- песочницу не запускаю`)
      await runPass(bin, role, width, port)
    }
  }
} catch (e) {
  console.error(e)
  record({ role: '-', width: 0, screen: 'run', problems: [{ check: 0, what: `прогон прерван: ${e.message}` }] })
}

flush()
const bad = report.filter((e) => e.problems.length)
// «пропущено» -- всегда 0: пропусков в обходе нет, строка -- для сверки с прежними прогонами.
console.log(`экранов: ${report.length}, с находками: ${bad.length}, пропущено: 0; отчёт и снимки: ${out}`)
process.exit(bad.length ? 1 : 0)
