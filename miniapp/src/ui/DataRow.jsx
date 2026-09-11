import { Quoted } from './Q.jsx'

// Строка данных. Правило дизайн-системы: строка ВСЕГДА несёт значение справа.
// Строки без значения не бывает -- это повод дописать бэкенд, а не поставить
// прочерк, поэтому value здесь обязателен по смыслу, а не по типу.
//
// Тексты строки собирают чистые функции, и в них бывают имена в «ёлочках»:
// они идут через <Quoted>, чтобы имя не рвалось по дефису.
export function DataRow({ dot, title, code, value, valueSub, valueTone }) {
  return (
    <div class="data-row">
      {dot ? <span class={`data-row-dot data-row-dot-${dot}`} /> : null}
      <span class="data-row-main">
        <Quoted text={title} />
        {code ? <u class="data-row-code">{code}</u> : null}
      </span>
      <span class={valueTone ? `data-row-value data-row-value-${valueTone}` : 'data-row-value'}>
        <Quoted text={value} />
        {valueSub ? (
          <span class="data-row-value-sub">
            <Quoted text={valueSub} />
          </span>
        ) : null}
      </span>
    </div>
  )
}
