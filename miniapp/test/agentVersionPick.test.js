import { describe, it, expect } from 'vitest'
import {
  parseVersion,
  normalizeVersion,
  compareVersions,
  versionPick,
  canPickVersion,
  otherVersionSheetText,
  otherVersionFields,
  otherVersionReady,
  otherVersionRequest,
} from '../src/agentVersionPick.js'

const HOME = { id: 22, nickname: 'home', agent_version: 'v0.35.0', pending_version: '' }
const BACKEND = 'v0.36.0'

describe('разбор и сравнение версий', () => {
  it('с «v» и без, с предрелизом; мусор -- null', () => {
    expect(parseVersion('v0.35.1')).toEqual({ major: 0, minor: 35, patch: 1, pre: '' })
    expect(parseVersion(' 0.36.0-rc1 ')).toEqual({ major: 0, minor: 36, patch: 0, pre: 'rc1' })
    expect(parseVersion('0.36')).toBe(null)
    expect(parseVersion('latest')).toBe(null)
    expect(normalizeVersion('0.34.0')).toBe('v0.34.0')
    expect(normalizeVersion('v0.36.0-rc1')).toBe('v0.36.0-rc1')
    expect(normalizeVersion('x')).toBe('')
  })

  it('сравнение по числам, предрелиз ниже своего релиза', () => {
    expect(compareVersions('v0.9.0', 'v0.10.0')).toBe(-1)
    expect(compareVersions('v0.35.0', '0.35.0')).toBe(0)
    expect(compareVersions('v1.0.0', 'v0.99.9')).toBe(1)
    expect(compareVersions('v0.36.0-rc1', 'v0.36.0')).toBe(-1)
    expect(compareVersions('v0.36.0', 'v0.36.0-rc1')).toBe(1)
    expect(compareVersions('v0.36.0-rc1', 'v0.36.0-rc2')).toBe(-1)
    expect(compareVersions('v0.36.0', 'мусор')).toBe(null)
  })
})

describe('versionPick', () => {
  const pick = (input) => versionPick(input, { current: 'v0.35.0', backend: BACKEND })

  it('пусто и негодно -- нельзя, с подсказкой формата', () => {
    expect(pick('')).toMatchObject({ state: 'empty', ok: false, hint: 'Наберите версию агента, например v0.36.0.' })
    expect(pick('0.35')).toMatchObject({ state: 'invalid', ok: false, hint: 'Версия пишется так: v0.36.0.' })
  })

  it('формат версии -- как у сервера (formRules): только -rcN', () => {
    expect(pick('v0.34.0-beta')).toMatchObject({ state: 'invalid', ok: false })
    expect(pick('v0.34.0-rc2')).toMatchObject({ state: 'downgrade', ok: true, version: 'v0.34.0-rc2' })
  })

  it('новее бэкенда выпуска нет', () => {
    expect(pick('v0.37.0')).toMatchObject({ state: 'ahead', ok: false, hint: 'Выпуска новее бэкенда (v0.36.0) нет.' })
  })

  it('та же версия -- нечего ставить', () => {
    expect(pick('0.35.0')).toMatchObject({ state: 'same', ok: false, hint: 'На роутере уже v0.35.0.' })
  })

  it('ниже текущей -- откат', () => {
    expect(pick('0.34.0')).toEqual({ version: 'v0.34.0', state: 'downgrade', ok: true, downgrade: true, hint: 'Это откат: на роутере v0.35.0.' })
  })

  it('выше текущей -- обычное обновление', () => {
    expect(pick('v0.36.0')).toEqual({ version: 'v0.36.0', state: 'upgrade', ok: true, downgrade: false, hint: 'Обновление с v0.35.0.' })
  })

  it('версия на роутере неизвестна -- откатом не считаем, сервер решит сам', () => {
    expect(versionPick('v0.30.0', { current: '', backend: BACKEND })).toMatchObject({ state: 'upgrade', ok: true, downgrade: false, hint: '' })
  })
})

describe('лист «Другая версия»', () => {
  it('кнопка -- только при известной версии, без отложенного обновления и не во время оживления', () => {
    expect(canPickVersion(HOME)).toBe(true)
    expect(canPickVersion({ ...HOME, pending_version: 'v0.36.0' })).toBe(false)
    expect(canPickVersion({ ...HOME, agent_version: '' })).toBe(false)
    expect(canPickVersion({ ...HOME, revive: { status: 'running' } })).toBe(false)
    expect(canPickVersion({ ...HOME, revive: { status: 'waiting' } })).toBe(false)
    expect(canPickVersion({ ...HOME, revive: { status: 'done' } })).toBe(true)
  })

  it('текст листа называет текущую версию и потолок', () => {
    const t = otherVersionSheetText(HOME, BACKEND)
    expect(t.title).toBe('Поставить другую версию агента на «home»?')
    expect(t.body).toContain('Сейчас на роутере v0.35.0.')
    expect(t.body).toContain('не новее v0.36.0')
  })

  it('поле отката видно только при откате, подсказка следует вводу', () => {
    const [version, allow] = otherVersionFields(HOME, BACKEND)
    expect(version).toMatchObject({ name: 'target_version', type: 'text' })
    expect(allow).toMatchObject({ name: 'allow_downgrade', type: 'toggle', initial: false })
    expect(allow.showIf({ target_version: 'v0.34.0' })).toBe(true)
    expect(allow.showIf({ target_version: 'v0.36.0' })).toBe(false)
    expect(version.hint({ target_version: 'v0.34.0' })).toBe('Это откат: на роутере v0.35.0.')
  })

  it('готовность: откат -- только с включённым переключателем', () => {
    const ready = otherVersionReady(HOME, BACKEND)
    expect(ready({ target_version: 'v0.36.0', allow_downgrade: false })).toBe(true)
    expect(ready({ target_version: 'v0.34.0', allow_downgrade: false })).toBe(false)
    expect(ready({ target_version: 'v0.34.0', allow_downgrade: true })).toBe(true)
    expect(ready({ target_version: 'v0.37.0', allow_downgrade: true })).toBe(false)
    expect(ready({ target_version: 'v0.35.0', allow_downgrade: true })).toBe(false)
  })

  it('запрос: разрешение отката уходит только вместе с откатом', () => {
    expect(otherVersionRequest({ target_version: '0.34.0', allow_downgrade: true }, HOME, BACKEND)).toEqual({ targetVersion: 'v0.34.0', allowDowngrade: true })
    expect(otherVersionRequest({ target_version: 'v0.36.0', allow_downgrade: true }, HOME, BACKEND)).toEqual({ targetVersion: 'v0.36.0', allowDowngrade: false })
  })
})
