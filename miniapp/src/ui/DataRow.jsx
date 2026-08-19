// Строка данных. Правило дизайн-системы: строка ВСЕГДА несёт значение справа.
// Строки без значения не бывает -- это повод дописать бэкенд, а не поставить
// прочерк, поэтому value здесь обязателен по смыслу, а не по типу.
export function DataRow({ dot, title, code, value, valueSub, valueTone }) {
  return (
    <div class="data-row">
      {dot ? <span class={`data-row-dot data-row-dot-${dot}`} /> : null}
      <span class="data-row-main">
        {title}
        {code ? <u class="data-row-code">{code}</u> : null}
      </span>
      <span class={valueTone ? `data-row-value data-row-value-${valueTone}` : 'data-row-value'}>
        {value}
        {valueSub ? <span class="data-row-value-sub">{valueSub}</span> : null}
      </span>
    </div>
  )
}
