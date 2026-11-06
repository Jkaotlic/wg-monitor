import { describe, expect, it } from 'vitest'
import { capTunnelState, statusEvidenceCopy } from '../src/statusEvidence.js'

describe('capTunnelState', () => {
  it('не повышает локально упавший туннель даже при здоровом backend-вердикте', () => {
    expect(capTunnelState('down', { state: 'healthy' })).toBe('down')
  })

  it.each(['degraded', 'stale', 'unknown'])('не рисует зелёный при backend state=%s', (state) => {
    expect(capTunnelState('up', { state })).toBe('unknown')
  })

  it('рисует падение, когда backend подтвердил отказ несущего', () => {
    expect(capTunnelState('up', { state: 'failed' })).toBe('down')
  })

  it('оставляет старые ответы совместимыми, если evidence ещё нет', () => {
    expect(capTunnelState('up', null)).toBe('up')
  })
})

describe('statusEvidenceCopy', () => {
  it('объясняет конфликт источников и сообщает число затронутых правил', () => {
    expect(statusEvidenceCopy({ reason_code: 'source_conflict', affected_routes: 18 })).toEqual({
      tone: 'warn',
      title: 'Источники расходятся',
      detail: '18 правил маршрутизации требуют проверки',
    })
  })

  it('различает адресную маршрутизацию и прямой путь', () => {
    expect(statusEvidenceCopy({ reason_code: 'address_routed' }).title).toBe('Маршрут выбирается по адресу')
    expect(statusEvidenceCopy({ reason_code: 'direct_path' }).title).toBe('Трафик идёт напрямую')
  })
})
