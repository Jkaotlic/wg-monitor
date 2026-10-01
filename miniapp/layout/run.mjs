// Скрипт раскладки (v0.52, спека §9): поднимает песочницу на каждую роль
// (порты 8121–8125), обходит SCREENS на ширинах 360/390/1024/1440, снимает
// каждый экран и падает при любой находке. В CI не входит, перед выпуском
// обязателен. Каждую свою песочницу гасит сам.
import { spawn, execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, openSync } from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { collectLayout, findProblems, netProblems, SMALL_OK, SKIP_TARGETS } from './checks.js'
import { ROLES, WIDTHS, SCREENS } from './screens.js'

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const args = Object.fromEntries(process.argv.slice(2).map((a, i, all) => (a.startsWith('--') ? [a.slice(2), all[i + 1]] : null)).filter(Boolean))
const roles = args.roles ? args.roles.split(',') : ROLES
const widths = args.widths ? args.widths.split(',').map(Number) : WIDTHS
const out = args.out ?? path.join(os.tmpdir(), `wgm-layout-${new Date().toISOString().replace(/[:.]/g, '-')}`)
mkdirSync(out, { recursive: true })

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

// Дефис в тексте шага -- как обычный, так и неразрывный (text.js).
const hy = (s) => s.replace(/-/g, '[-‑]')

async function clickText(page, text) {
  const handle = await page.evaluateHandle((t) => {
    // Интерфейс печатает «VPN‑туннель» с неразрывным дефисом (text.js) -- сравниваем без него.
    const norm = (s) => (s || '').trim().replace(/\s+/g, ' ').replace(/[‐‑]/g, '-')
    const els = [...document.querySelectorAll('button, a[href], [role=tab], summary')].filter((e) => e.offsetParent !== null || getComputedStyle(e).position === 'fixed')
    return els.find((e) => norm(e.innerText) === t) ?? els.find((e) => norm(e.innerText).startsWith(t)) ?? null
  }, text)
  const el = handle.asElement()
  if (!el) return false
  await el.click()
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

async function pickRouter(page, name) {
  const current = await page.evaluate(() => (document.querySelector('.app-header-title, .router-switch-name, .main-head-name')?.textContent ?? '').trim())
  if (current === name) return true
  const side = await page.$(`.side-row:has-text("${name}")`)
  if (side) { await side.click(); await page.waitForTimeout(700); return true }
  const chip = await page.$(`.strip-chip:has-text("${name}")`)
  if (chip) { await chip.click(); await page.waitForTimeout(700); return true }
  const sw = await page.$('.router-switch')
  if (!sw) return false
  await sw.click()
  await page.waitForTimeout(500)
  const row = await page.$(`.fleet-row:has-text("${name}")`)
  if (!row) return false
  await row.click()
  await page.waitForTimeout(700)
  return true
}

async function runStep(page, step) {
  if (step.tab) {
    if (await clickText(page, step.tab)) return true
    // Админ без роутера уже на Парке, панели вкладок нет (одна вкладка).
    return step.tab === 'Парк' && Boolean(await page.$('.park-tab'))
  }
  if (step.click) return clickText(page, step.click)
  if (step.segment) return clickText(page, step.segment)
  if (step.headerPick) {
    // На широкой раскладке список роутеров -- колонка, он и так на экране.
    if (await page.$('.wide-shell')) return true
    const sw = await page.$('.router-switch')
    if (!sw) return false
    await sw.click()
    await page.waitForTimeout(600)
    return true
  }
  if (step.sheetChoice) {
    for (const c of await page.$$('.sheet-choice')) {
      if ((await c.innerText()).trim().startsWith(step.sheetChoice)) {
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
  if (step.rowIn) {
    const row = await page.$(`.section:has(.section-title:text-matches("^${hy(step.rowIn)}")) .list-row-btn`)
    if (!row) return false
    await row.click()
    await page.waitForTimeout(700)
    return true
  }
  if (step.tunnelWith) {
    const n = (await page.$$('.section:has(.section-title:text-matches("^Все VPN[-‑]туннели")) .list-row-btn')).length
    for (let i = 0; i < n; i++) {
      const rows = await page.$$('.section:has(.section-title:text-matches("^Все VPN[-‑]туннели")) .list-row-btn')
      await rows[i].click()
      await page.waitForTimeout(700)
      if (await page.$(`.overlay button:text-matches("^${hy(step.tunnelWith)}$")`)) return true
      await reset(page)
    }
    return false
  }
  throw new Error(`неизвестный шаг ${JSON.stringify(step)}`)
}

async function runRole(role, port, report) {
  const browser = await chromium.launch()
  const page = await (await browser.newContext({ viewport: { width: widths[0], height: 800 } })).newPage()
  let events = []
  page.on('console', (m) => { if (m.type() === 'error') events.push({ kind: 'console', text: m.text() }) })
  page.on('pageerror', (e) => events.push({ kind: 'console', text: e.message }))
  page.on('response', (r) => { if (r.status() >= 400) events.push({ kind: 'http', status: r.status(), method: r.request().method(), url: new URL(r.url()).pathname }) })
  // Один вход на роль: у песочницы лимит входов (10, затем 1 в 5 с).
  await page.goto(`http://127.0.0.1:${port}/miniapp/`)
  await page.waitForSelector('.app-header, .wide-shell', { timeout: 15000 })
  try {
    for (const width of widths) {
      await page.setViewportSize({ width, height: width < 1024 ? 800 : 900 })
      await page.waitForTimeout(500)
      for (const screen of SCREENS.filter((s) => s.roles.includes(role))) {
        await reset(page)
        events = []
        // Роутер экрана выбирают только там, где выбор есть (админ, 2–5, 6+);
        // у владельца одного роутера и у оператора открыт свой -- он и снимается.
        let opened = !screen.router || (await pickRouter(page, screen.router)) || role !== 'admin'
        for (const step of screen.steps) {
          if (!opened) break
          opened = await runStep(page, step)
        }
        const entry = { role, width, screen: screen.id, problems: [] }
        if (!opened) {
          if (!screen.optional) entry.problems.push({ check: 0, what: 'экран не открылся по шагам обхода' })
          else entry.skipped = true
        } else {
          await page.waitForTimeout(600)
          const data = await page.evaluate(collectLayout, { smallOk: SMALL_OK, skip: SKIP_TARGETS })
          entry.problems.push(...findProblems(data), ...netProblems(events))
          const dir = path.join(out, role, String(width))
          mkdirSync(dir, { recursive: true })
          await page.screenshot({ path: path.join(dir, `${screen.id}.png`), fullPage: true })
        }
        report.push(entry)
      }
    }
  } finally {
    await browser.close()
  }
}

const bin = buildSandbox()
const report = []
for (const [i, role] of roles.entries()) {
  const port = 8121 + i
  if (await portBusy(port)) throw new Error(`порт ${port} занят чужим процессом -- песочницу не запускаю`)
  mkdirSync(path.join(out, role), { recursive: true })
  const log = openSync(path.join(out, role, 'sandbox.log'), 'w')
  const child = spawn(bin, ['-addr', `127.0.0.1:${port}`, '-role', role], { cwd: REPO, detached: true, stdio: ['ignore', log, log] })
  try {
    await waitHTTP(`http://127.0.0.1:${port}/miniapp/`, 60000)
    await runRole(role, port, report)
  } finally {
    process.kill(-child.pid, 'SIGTERM')
  }
}

const bad = report.filter((e) => e.problems.length)
writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2))
const lines = bad.flatMap((e) => e.problems.map((p) => `${e.role}\t${e.width}\t${e.screen}\t[${p.check}] ${p.what}`))
const skipped = report.filter((e) => e.skipped).map((e) => `${e.role}\t${e.width}\t${e.screen}\tпропущен (optional)`)
writeFileSync(path.join(out, 'report.txt'), [...lines, ...skipped].join('\n') + '\n')
console.log(`экранов: ${report.length}, с находками: ${bad.length}, пропущено: ${skipped.length}; отчёт и снимки: ${out}`)
process.exit(bad.length ? 1 : 0)
