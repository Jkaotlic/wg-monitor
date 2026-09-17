import { useState } from 'preact/hooks'
import { applyFleetFilter } from './fleetFilter.js'

// Состояние поиска одного списка. Живёт в компоненте списка: в адрес и в
// навигацию не попадает.
export function useFleetFilter(routers) {
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('all')
  const view = applyFleetFilter(routers, { query, filter })
  function reset() {
    setQuery('')
    setFilter('all')
  }
  return { query, setQuery, filter, setFilter, view, reset }
}
