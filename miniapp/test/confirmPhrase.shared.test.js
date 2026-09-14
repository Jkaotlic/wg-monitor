import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { confirmReady } from '../src/sheet.js'

// Те же случаи прогоняет TestConfirmPhraseSharedCases на бэкенде: экран и
// сервер обязаны соглашаться, иначе человек увидит активную кнопку и отказ.
const cases = JSON.parse(
  readFileSync(new URL('../../internal/backend/testdata/confirm_phrase_cases.json', import.meta.url), 'utf8'),
)

describe('подтверждение набором: общие случаи с сервером', () => {
  it('файл случаев на месте', () => {
    expect(cases.length).toBeGreaterThanOrEqual(8)
  })
  for (const c of cases) {
    it(c.name, () => {
      expect(confirmReady({ confirmPhrase: c.phrase }, c.typed)).toBe(c.ok)
    })
  }
})
