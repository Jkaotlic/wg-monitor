// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { FleetCounts } from '../src/ui/FleetCounts.jsx'
import { fleetSummary } from '../src/fleet.js'
import { applyFleetFilter, FLEET_FILTERS } from '../src/fleetFilter.js'

// A1.6 (v0.55): плитка «Парка» и чипы фильтра говорят одними словами и
// считают одинаково; спящие -- отдельной плиткой, и только когда они есть.
const R = (id, status, age) => ({ id, nickname: `r${id}`, status, last_seen_age_sec: age })
const mount = async (summary) => {
  const root = document.createElement('div')
  await act(async () => render(<FleetCounts summary={summary} />, root))
  return root
}
const tiles = (root) => [...root.querySelectorAll('.fleet-count')].map((t) => [t.querySelector('.fleet-count-value').textContent, t.querySelector('.fleet-count-label').textContent])

describe('плитки «Парка»', () => {
  it('без спящих -- три плитки, как раньше', async () => {
    const root = await mount(fleetSummary([R(1, 'online', 5), R(2, 'offline', 9000)]))
    expect(tiles(root)).toEqual([['1', 'в порядке'], ['0', 'тревог'], ['1', 'молчит']])
  })

  it('спящие -- своя плитка «спят», молчащие считаются без них; те же числа, что в чипах', async () => {
    const list = [R(1, 'online', 5), R(2, 'sleeping', 3600), R(3, 'sleeping', 3600), R(4, 'offline', 9000)]
    const root = await mount(fleetSummary(list))
    expect(tiles(root)).toEqual([['1', 'в порядке'], ['0', 'тревог'], ['2', 'спят'], ['1', 'молчит']])
    const { counts } = applyFleetFilter(list)
    expect(counts.sleeping).toBe(2)
    expect(counts.silent).toBe(1)
    expect(FLEET_FILTERS.find((f) => f.key === 'sleeping').label).toBe('спят')
    expect(FLEET_FILTERS.find((f) => f.key === 'silent').label).toBe('молчат')
  })
})
