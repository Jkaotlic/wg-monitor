import { describe, it, expect } from 'vitest'
import { ISSUER_PANEL_DOWN, awg3IssueErrorText, awg3ErrorText } from '../src/awg3Panel.js'
import { issueFailure, cabinetPerms } from '../src/cabinetKeys.js'

const err = (code, serverMessage = '') => ({ code, serverMessage })

describe('v0.52 §8: допущенный не видит админских текстов панели', () => {
  it.each(['awg3_bad_password', 'awg3_disabled', 'awg3_cert_rejected', 'awg3_unreachable', 'awg3_paused'])('%s', (code) => {
    expect(awg3IssueErrorText(err(code, 'Неверный пароль панели — пересохраните учётные данные'), { admin: false })).toBe(ISSUER_PANEL_DOWN)
    expect(awg3IssueErrorText(err(code), { admin: true })).toBe(awg3ErrorText(err(code)))
  })
  it('только для просмотра -- правда и допущенному', () => {
    expect(awg3IssueErrorText(err('awg3_readonly'), { admin: false })).toBe(awg3ErrorText(err('awg3_readonly')))
  })
  it('выпуск с панели: отказ панели допущенному -- одна фраза', () => {
    expect(issueFailure(err('awg3_bad_password', 'пересохраните'), cabinetPerms('operator'), 'awg3panel').text).toBe(ISSUER_PANEL_DOWN)
    expect(issueFailure(err('awg3_bad_password', 'пересохраните'), cabinetPerms('admin'), 'awg3panel').text).toBe(awg3ErrorText(err('awg3_bad_password')))
    expect(ISSUER_PANEL_DOWN).not.toMatch(/пересохраните|включите/)
  })
})
