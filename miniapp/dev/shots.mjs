// Снимает экраны мини-аппа с dev-сервера (фикстуры из dev/fixtures.js).
// Запуск: npm run dev &  затем  node dev/shots.mjs [базовый-url]
//
// Нужен, потому что "посмотрел код" доказательством не является: вёрстку в
// двух темах и на узком экране надо увидеть. Снимки кладутся в dev/shots/
// и в git не попадают (см. .gitignore).
import { mkdirSync } from 'node:fs'
import { chromium } from 'playwright'

const base = process.argv[2] ?? 'http://localhost:5173/miniapp/?router=1'
const outDir = new URL('./shots/', import.meta.url).pathname
mkdirSync(outDir, { recursive: true })

const SHOTS = [
  { name: 'router', steps: async () => {} },
  { name: 'fleet', overlay: true, steps: async (page) => page.getByRole('button', { name: 'Мои роутеры' }).click() },
  { name: 'admin', overlay: true, steps: async (page) => page.getByRole('button', { name: /Обслуживание/ }).click({ timeout: 2000 }) },
  {
    name: 'sheet',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Перезапустить туннель' }).first().click({ timeout: 2000 })
    },
  },
  { name: 'routes', steps: async (page) => page.getByRole('button', { name: 'Маршруты' }).click() },
  {
    name: 'diag',
    steps: async (page) => {
      await page.getByRole('button', { name: 'Диагностика' }).click()
      await page.getByRole('button', { name: 'Собрать отчёт' }).click()
      await page.waitForTimeout(600)
    },
  },
  { name: 'events', steps: async (page) => page.getByRole('button', { name: 'События', exact: true }).click() },
]

const browser = await chromium.launch()
for (const scheme of ['light', 'dark']) {
  const context = await browser.newContext({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 2,
    colorScheme: scheme,
  })
  for (const shot of SHOTS) {
    const page = await context.newPage()
    await page.goto(base, { waitUntil: 'networkidle' })
    try {
      await shot.steps(page)
    } catch (err) {
      // Экран, которого ещё нет, не должен ронять весь прогон: снимаем то,
      // что удалось открыть, и говорим, чего не хватило.
      console.warn(`${shot.name}/${scheme}: ${err.message.split('\n')[0]}`)
    }
    await page.waitForTimeout(250)
    // Оверлей перекрывает экран целиком, но fullPage снимает и то, что под
    // ним -- для таких снимков берём ровно видимую область.
    await page.screenshot({ path: `${outDir}${shot.name}-${scheme}.png`, fullPage: !shot.overlay })
    await page.close()
  }
  await context.close()
}
await browser.close()
console.log('снимки в', outDir)
