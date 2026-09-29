// Три числа парка -- ответ на «всё ли в порядке»: в порядке, требуют
// внимания, молчат. Одни и те же на сводке широкого экрана и во вкладке
// «Парк» (v0.48), поэтому один компонент.
export function FleetCounts({ summary }) {
  return (
    <div class="fleet-counts">
      <div class="fleet-count fleet-count-ok">
        <span class="fleet-count-value">{summary.ok}</span>
        <span class="fleet-count-label">в порядке</span>
      </div>
      <div class="fleet-count fleet-count-attention">
        <span class="fleet-count-value">{summary.attention}</span>
        <span class="fleet-count-label">требуют внимания</span>
      </div>
      <div class="fleet-count fleet-count-silent">
        <span class="fleet-count-value">{summary.silent}</span>
        <span class="fleet-count-label">молчат</span>
      </div>
    </div>
  )
}
