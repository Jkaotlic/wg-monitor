// Три числа парка -- ответ на «всё ли в порядке»: в порядке, требуют
// внимания, молчат. Одни и те же на сводке широкого экрана и во вкладке
// «Парк» (v0.48), поэтому один компонент.
import { stateCountLabel } from '../fleet.js'

// Подписи -- из одного словаря парка (v0.50).
export function FleetCounts({ summary }) {
  return (
    <div class="fleet-counts" style={summary.sleeping > 0 ? '--fleet-count-cols:4' : undefined}>
      <div class="fleet-count fleet-count-ok">
        <span class="fleet-count-value">{summary.ok}</span>
        <span class="fleet-count-label">{stateCountLabel('ok', summary.ok)}</span>
      </div>
      <div class="fleet-count fleet-count-attention">
        <span class="fleet-count-value">{summary.attention}</span>
        <span class="fleet-count-label">{stateCountLabel('attention', summary.attention)}</span>
      </div>
      {summary.sleeping > 0 && (
        <div class="fleet-count fleet-count-sleeping">
          <span class="fleet-count-value">{summary.sleeping}</span>
          <span class="fleet-count-label">{stateCountLabel('sleeping', summary.sleeping)}</span>
        </div>
      )}
      <div class="fleet-count fleet-count-silent">
        <span class="fleet-count-value">{summary.silent}</span>
        <span class="fleet-count-label">{stateCountLabel('silent', summary.silent)}</span>
      </div>
    </div>
  )
}
