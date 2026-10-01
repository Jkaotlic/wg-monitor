// @vitest-environment jsdom
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it, expect } from 'vitest'
import { returnLabel } from '../src/screens/OverlayHost.jsx'
import { ACTION_LABELS } from '../src/labels.js'

// vitest.config: тесты идут из miniapp/; под jsdom import.meta.url не файловый.
const SRC = join(process.cwd(), 'src') + '/'

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
  [/VPN-туннель из кабинета/, '«Новый VPN-туннель»'],
  [/Кабинеты VPN/, '«Откуда взять конфиг»'],
  [/(['"`>])Все роутеры(['"`<])/, '«Мои роутеры» / «Парк»'],
  [/Проверить сейчас/, '«Проверить заново» / «Сравнить адреса выхода»'],
  [/Сверить версии сейчас/, '«Сверить версии»'],
  [/Проверить роутер изнутри/, '«Осмотр изнутри»'],
  [/Осмотр всех/, '«Осмотреть все»'],
  [/Панели awg3/, '«Панели VPN-серверов»'],
  [/(['"`>])Управление(['"`<])/, '«Настройки»'],
  [/«Управление»/, '«Настройки»'],
]

const ALL = files(SRC).map((p) => [p.slice(SRC.length), code(readFileSync(p, 'utf8'))])

describe('словарь v0.52: старых подписей на экране нет', () => {
  it.each(FORBIDDEN.map(([re, instead]) => [String(re), re, instead]))('%s', (_name, re, instead) => {
    expect(ALL.filter(([, src]) => re.test(src)).map(([f]) => f), `замена: ${instead}`).toEqual([])
  })
  it('подписи возврата слоёв -- по словарю', () => {
    expect(['park', 'fleet', 'manage', 'selfhosted', 'awg3panel', null].map((to) => returnLabel(to))).toEqual(['Парк', 'Мои роутеры', 'Настройки', 'Серверы', 'Панель VPN-сервера', 'Роутеры'])
    expect(ACTION_LABELS.recheck).toBe('Проверить заново')
  })
})
