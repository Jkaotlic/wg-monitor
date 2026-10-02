// @vitest-environment jsdom
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, it, expect } from 'vitest'
import { returnLabel } from '../src/screens/OverlayHost.jsx'
import { ACTION_LABELS } from '../src/labels.js'

// От места самого теста, а не от process.cwd(): vitest зовут и из miniapp/, и
// из корня репозитория. Путь -- строкой: под jsdom глобальный URL не узловой,
// и fileURLToPath(new URL(...)) падает, а строку import.meta.url принимает.
const SRC = join(dirname(fileURLToPath(import.meta.url)), '..', 'src') + '/'

function files(dir) {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    if (statSync(p).isDirectory()) return files(p)
    return /\.(js|jsx)$/.test(f) ? [p] : []
  })
}

// Комментарии -- не экран: старые слова в них остаются историей решений.
function code(src) {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`\\])\/\/.*$/gm, '$1')
}

// Спека §5, §10: одно имя на действие; старые подписи не возвращаются.
const FORBIDDEN = [
  [/Повторить проверку/, '«Проверить заново»'],
  [/Опросить все/, '«Проверить заново все»'],
  [/Собрать диагностику/, '«Собрать отчёт»'],
  [/из кабинета/, '«Новый VPN-туннель» → «Откуда взять конфиг» (слово «кабинет» -- не в навигации)'],
  [/загружать конфиги могут/, 'оператор тоже загружает .conf'],
  [/Кабинеты VPN/, '«Откуда взять конфиг»'],
  [/(['"`>])Все роутеры(['"`<])/, '«Мои роутеры» / «Парк»'],
  [/Проверить сейчас/, '«Проверить заново» / «Сравнить адреса выхода»'],
  [/Сверить версии сейчас/, '«Сверить версии»'],
  [/Проверить роутер изнутри/, '«Осмотр изнутри»'],
  [/Осмотр всех/, '«Осмотреть все»'],
  [/Панели awg3/, '«Панели VPN-серверов»'],
  [/(['"`>])Управление(['"`<])/, '«Настройки»'],
  [/«Управление»/, '«Настройки»'],
  [/Свои серверы/, '«Свои VPS» / «Серверы»'],
  [/владелец роутера или администратор/, 'с оператором: «владелец роутера, оператор или администратор»'],
  [/Парк → «Открыть в браузере»/, 'placeText(\'browser\')'],
  [/вкладке «Свой сервер» его кабинета/, '«Новый VPN-туннель» → «Откуда взять конфиг»'],
]

// Что найдено в исходнике: номера запрещённых подписей.
// Подпись в JSX часто разбита: перенос посреди текста, текст на своей строке
// между тегами, склейка строк через + или {' '}. Перед поиском исходник
// сплющивается -- только для поиска, смысл кода тут не важен.
function flat(src) {
  return code(src)
    .replace(/\{\s*(['"`])(\s*)\1\s*\}/g, ' ')
    .replace(/(['"`])\s*\+\s*(['"`])/g, '')
    .replace(/\s+/g, ' ')
    .replace(/>\s+/g, '>')
    .replace(/\s+</g, '<')
}

function scan(src) {
  const text = flat(src)
  return FORBIDDEN.flatMap(([re], i) => (re.test(text) ? [i] : []))
}

const ALL = files(SRC).map((p) => [p.slice(SRC.length), readFileSync(p, 'utf8')])
const at = (label) => FORBIDDEN.findIndex(([re]) => re.source.includes(label))

describe('словарь v0.52: сам сторож ловит подпись, разбитую по строкам', () => {
  it('исходники найдены от места теста', () => {
    expect(ALL.length).toBeGreaterThan(100)
    expect(ALL.map(([f]) => f)).toContain('nav.js')
  })
  it('текст JSX с переносом строки посреди подписи', () => {
    expect(scan('<p class="hint">\n        Повторить\n        проверку можно через минуту\n      </p>')).toEqual([at('Повторить проверку')])
  })
  it('подпись между тегами на своей строке', () => {
    expect(scan('<h1 class="screen-title">\n          Все роутеры\n        </h1>')).toEqual([at('Все роутеры')])
    expect(scan('<button type="button">\n  Управление\n</button>')).toEqual([at(')Управление(')])
  })
  it('подпись, склеенная из строк через + и через {\' \'}', () => {
    expect(scan("const t = 'Собрать ' +\n  'диагностику'")).toEqual([at('Собрать диагностику')])
    expect(scan("<p>Опросить{' '}\n  все</p>")).toEqual([at('Опросить все')])
  })
  it('комментарий и чистый текст не срабатывают', () => {
    expect(scan('// Повторить проверку -- старое имя\n{/* Все роутеры */}\nconst a = 1')).toEqual([])
    expect(scan('<p>\n  Проверить\n  заново\n</p>')).toEqual([])
  })
})

describe('словарь v0.52: старых подписей на экране нет', () => {
  it.each(FORBIDDEN.map(([re, instead], i) => [String(re), i, instead]))('%s', (_name, i, instead) => {
    expect(ALL.filter(([, src]) => scan(src).includes(i)).map(([f]) => f), `замена: ${instead}`).toEqual([])
  })
  it('подписи возврата слоёв -- по словарю', () => {
    expect(['park', 'fleet', 'manage', 'selfhosted', 'awg3panel', null].map((to) => returnLabel(to))).toEqual(['Парк', 'Мои роутеры', 'Настройки', 'Серверы', 'Панель VPN-сервера', 'Роутеры'])
    expect(returnLabel('fleet')).toBe('Мои роутеры')
    expect(returnLabel('fleet', true)).toBe('Выбрать роутер')
    expect(['vps', 'awg3', 'all'].map((p) => returnLabel('selfhosted', true, p))).toEqual(['Свои VPS', 'Панели VPN-серверов', 'Серверы'])
    expect(ACTION_LABELS.recheck).toBe('Проверить заново')
  })
})
