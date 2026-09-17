import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Цикл 2 (часть 3): новые классы есть, телефонные -- вне блока ≥1024px,
// правила колонки -- внутри него. Задачи 2–6 дописывают сюда свои describe.
export const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')
const OPEN = '@media (min-width: 1024px) {'

function mediaBlock(text) {
  const start = text.indexOf(OPEN)
  if (start < 0) return null
  let depth = 0
  for (let i = start + OPEN.length - 1; i < text.length; i++) {
    if (text[i] === '{') depth++
    else if (text[i] === '}') {
      depth--
      if (depth === 0) return { start, end: i + 1, body: text.slice(start, i + 1) }
    }
  }
  return null
}

const block = mediaBlock(css)
const outside = css.slice(0, block.start) + css.slice(block.end)

// Тело правила по точному селектору ("\n.filter-chips {" или "\n  .side-filter {").
function rule(text, selector) {
  const at = text.indexOf(`${selector} {`)
  if (at < 0) return null
  return text.slice(at, text.indexOf('}', at) + 1)
}

describe('поиск и фильтры: CSS', () => {
  it('телефонные классы -- вне блока широкой раскладки', () => {
    for (const sel of ['.filter-bar', '.filter-search', '.filter-chips', '.filter-chip', '.filter-chip-active', '.filter-chip-count', '.filter-empty']) {
      expect(rule(outside, `\n${sel}`), sel).not.toBe(null)
    }
  })

  it('чипы переносятся, а не прокручиваются вбок', () => {
    const chips = rule(outside, '\n.filter-chips')
    expect(chips).toMatch(/flex-wrap:\s*wrap/)
    expect(chips).not.toMatch(/overflow-x/)
  })

  it('поиск в колонке -- внутри блока ≥1024px', () => {
    expect(rule(block.body, '\n  .side-filter')).not.toBe(null)
    expect(block.body).toMatch(/\.side-filter \.filter-chip\s*\{/)
  })
})

describe('Парк: сторож и отложенное, CSS', () => {
  it('мостика на классическое больше нет', () => {
    expect(css.includes('.park-classic')).toBe(false)
  })

  it('строка сторожа оформлена на любой ширине', () => {
    for (const sel of ['.park-watchdog', '.park-watchdog-line', '.park-watchdog-warn .park-watchdog-line', '.park-watchdog-danger .park-watchdog-line']) {
      expect(rule(outside, `\n${sel}`), sel).not.toBe(null)
    }
  })
})

describe('лист: переключатель и подсказка поля, CSS', () => {
  it('классы оформлены на любой ширине', () => {
    for (const sel of ['.sheet-toggle-label', '.sheet-toggle-label input', '.sheet-field-hint']) {
      expect(rule(outside, `\n${sel}`), sel).not.toBe(null)
    }
  })
})

describe('«Опасное» в Обслуживании, CSS', () => {
  it('свёрнутый блок оформлен на любой ширине', () => {
    for (const sel of ['.danger-zone', '.danger-zone > summary', '.danger-zone[open] > summary']) {
      expect(rule(outside, `\n${sel}`), sel).not.toBe(null)
    }
  })
})

describe('«Пакеты по расписанию», CSS', () => {
  it('классы экрана -- вне блока широкой раскладки', () => {
    for (const sel of ['.packages-card + .packages-card', '.packages-time input', '.packages-actions', '.packages-log', '.packages-details > summary']) {
      expect(rule(outside, `\n${sel}`), sel).not.toBe(null)
    }
  })

  it('кнопки переносятся, журнал не раздвигает страницу вбок', () => {
    expect(rule(outside, '\n.packages-actions')).toMatch(/flex-wrap:\s*wrap/)
    expect(rule(outside, '\n.packages-log')).toMatch(/overflow-wrap:\s*anywhere/)
  })

  it('на широком экране журналу больше высоты', () => {
    expect(rule(block.body, '\n  .wide-shell .packages-log')).toMatch(/max-height/)
  })
})

describe('«Скопировать команды», CSS', () => {
  it('блок команд переносит длинные строки и оформлен на любой ширине', () => {
    expect(rule(outside, '\n.dns-commands')).toMatch(/overflow-wrap:\s*anywhere/)
    expect(rule(outside, '\n.dns-copy')).not.toBe(null)
  })
})

describe('осмотр приёмки: CSS', () => {
  it('итог действия -- строкой у кнопки, а не пустым экраном по центру', () => {
    expect(rule(outside, '\n.result-note')).toMatch(/text-align:\s*left/)
    expect(rule(outside, '\n.result-note-error')).not.toBe(null)
  })

  it('переключатель листа -- обычной строкой, не надписью поля', () => {
    // .field label (0,1,1) сильнее одиночного класса -- правило обязано быть не слабее.
    expect(rule(outside, '\n.field label.sheet-toggle-label')).toMatch(/text-transform:\s*none/)
  })

  it('команда установки не растягивает экран токена', () => {
    expect(rule(outside, '\n.token-command')).toMatch(/max-height/)
    // Строки скрипта не рвутся: прокрутка вбок только внутри блока.
    const cmd = rule(outside, '\n.token-block .token-command')
    expect(cmd).toMatch(/white-space:\s*pre;/)
    expect(cmd).toMatch(/overflow-x:\s*auto/)
    expect(cmd).toMatch(/overflow-wrap:\s*normal/)
    expect(rule(outside, '\n.token-block code')).toMatch(/min-width:\s*0/)
  })

  it('строка сторожа -- без второй линии и с отступом строки данных', () => {
    const wd = rule(outside, '\n.park-watchdog')
    expect(wd).not.toMatch(/border-top/)
    expect(wd).toMatch(/padding:\s*var\(--sp-3\) 14px 0/)
  })

  it('подсказка провала задания -- карточкой', () => {
    expect(rule(outside, '\n.job-hint')).toMatch(/border-left/)
  })

  it('«Опасное» на широком экране без лишнего отступа секции', () => {
    expect(block.body).toMatch(/\.wide-shell \.danger-zone > \.section\s*\{\s*margin-top:\s*0/)
  })
})

describe('ожидание раскатки на широком экране', () => {
  it('на всю ширину: ограничение основной области его не касается', () => {
    expect(block.body).toMatch(/\.main-content-narrow > \.deploy-wait\s*\{\s*max-width:\s*none/)
  })
})

describe('осмотр приёмки, круг 2', () => {
  it('адрес бэкенда на экране токена переносится под подпись, а не сжимает её', () => {
    // Телефон: подпись над значением, адрес моноширинным с переносом по символу.
    const row = rule(outside, '\n.token-backend .data-row')
    expect(row).toMatch(/flex-direction:\s*column/)
    const value = rule(outside, '\n.token-backend .data-row-value')
    expect(value).toMatch(/font-family:\s*var\(--font-mono\)/)
    expect(value).toMatch(/white-space:\s*normal/)
    expect(value).toMatch(/word-break:\s*break-all/)
    expect(value).toMatch(/text-align:\s*left/)
    // Широкая раскладка: снова в строку.
    expect(block.body).toMatch(/\.wide-shell \.token-backend \.data-row\s*\{\s*flex-direction:\s*row/)
  })

  it('поиск в «Моих роутерах» отделён от строки итога', () => {
    expect(rule(outside, '\n.screen > .filter-bar')).toMatch(/margin-top/)
  })
})
