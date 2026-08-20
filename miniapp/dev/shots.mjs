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
  // exact: обычное имя ловит ещё и карточку перехода "Туннели и резерв" на
  // главном экране, и снимок падал на неоднозначности.
  { name: 'tunnels', steps: async (page) => page.getByRole('button', { name: 'Туннели', exact: true }).click() },
  // Диагностика открывается сразу: числа берутся из того, что роутер уже
  // прислал, и команда нужна только чтобы переспросить.
  {
    name: 'diag',
    steps: async (page) => {
      await page.getByRole('button', { name: 'Диагностика' }).click()
      await page.waitForTimeout(500)
    },
  },
  {
    name: 'diag-exits',
    steps: async (page) => {
      await page.getByRole('button', { name: 'Диагностика' }).click()
      await page.waitForTimeout(500)
      await page.getByRole('button', { name: 'Сравнить адреса' }).click()
      await page.waitForTimeout(1200)
    },
  },
  {
    name: 'diag-report',
    steps: async (page) => {
      await page.getByRole('button', { name: 'Диагностика' }).click()
      await page.waitForTimeout(500)
      await page.getByRole('button', { name: 'Собрать отчёт' }).click()
      await page.waitForTimeout(900)
    },
  },
  { name: 'events', steps: async (page) => page.getByRole('button', { name: 'События', exact: true }).click() },
  // Маршруты и правка маршрутов: экран с кнопками правки, каталог наборов и
  // превью с пересечением -- три состояния, которые ломаются молча, если
  // смотреть только на тесты.
  {
    name: 'routes',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Туннели', exact: true }).click()
      await page.getByRole('button', { name: /Маршруты|Что уходит/ }).first().click({ timeout: 2000 })
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'route-catalog',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Туннели', exact: true }).click()
      await page.getByRole('button', { name: /Маршруты|Что уходит/ }).first().click({ timeout: 2000 })
      await page.waitForTimeout(900)
      await page.getByRole('button', { name: 'Отправить в туннель' }).first().click()
      await page.locator('.overlay').last().getByRole('button', { name: /awg3-work-via-ru1/ }).click()
      await page.waitForTimeout(800)
    },
  },
  {
    name: 'cabinet',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Туннели', exact: true }).click()
      await page.waitForTimeout(800)
      await page.getByRole('button', { name: /Новая линия из кабинета/ }).click()
      await page.waitForTimeout(800)
    },
  },
  {
    name: 'fleet-dots',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Мои роутеры' }).click()
      await page.waitForTimeout(500)
    },
  },
  {
    name: 'settings',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Настройки' }).click()
      await page.waitForTimeout(600)
      await page.getByRole('button', { name: 'Сверить версии' }).click()
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'route-preview-blocked',
    overlay: true,
    steps: async (page) => {
      await page.getByRole('button', { name: 'Туннели', exact: true }).click()
      await page.getByRole('button', { name: /Маршруты|Что уходит/ }).first().click({ timeout: 2000 })
      await page.waitForTimeout(900)
      await page.getByRole('button', { name: 'Отправить в туннель' }).first().click()
      await page.locator('.overlay').last().getByRole('button', { name: /awg3-work-via-ru1/ }).click()
      await page.waitForTimeout(800)
      await page.locator('.overlay').last().getByRole('button', { name: /^ChatGPT/ }).click()
      await page.waitForTimeout(800)
    },
  },
]

// Два состояния, которых в фикстурах нет по смыслу: пустой доступ и ровно
// один роутер. Подменяем ответ списка прямо в браузере, чтобы не заводить
// ради них второй набор фикстур.
const OVERRIDES = [
  // Первый вход: доступа нет вовсе -- отдельный экран, а не список из нуля
  // строк.
  { name: 'no-access', routers: [] },
  {
    name: 'single-router',
    routers: [{ id: 1, nickname: 'Дом', status: 'online', last_seen_age_sec: 12 }],
  },
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
  for (const o of OVERRIDES) {
    const page = await context.newPage()
    await page.route('**/v1/miniapp/routers', (route) =>
      route.fulfill({ contentType: 'application/json', body: JSON.stringify({ routers: o.routers }) }),
    )
    await page.goto(base.split('?')[0], { waitUntil: 'networkidle' })
    await page.waitForTimeout(250)
    await page.screenshot({ path: `${outDir}${o.name}-${scheme}.png` })
    await page.close()
  }
  await context.close()
}
await browser.close()
console.log('снимки в', outDir)
