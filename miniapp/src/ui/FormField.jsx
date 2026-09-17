// Поля полноэкранных форм: мастер «Добавить роутер», «Подключение агента».
// Лист подтверждения рисует свои поля сам (Sheet.jsx); классы те же -- .field,
// чтобы поле везде выглядело одинаково. Значение живёт у экрана-владельца:
// поле его не хранит, и пароль не уходит дальше экрана.
export function TextField({ id, label, value, onInput, type = 'text', placeholder = '', hint = '', inputMode }) {
  return (
    <div class="field">
      <label for={id}>{label}</label>
      <input
        id={id}
        type={type === 'password' ? 'password' : 'text'}
        autocomplete="off"
        autocapitalize="off"
        spellcheck={false}
        inputMode={inputMode}
        placeholder={placeholder}
        value={value ?? ''}
        onInput={(e) => onInput(e.currentTarget.value)}
      />
      {hint && <p class="field-hint">{hint}</p>}
    </div>
  )
}

export function SelectField({ id, label, value, onChange, options = [], hint = '' }) {
  return (
    <div class="field">
      <label for={id}>{label}</label>
      <select id={id} value={value ?? ''} onChange={(e) => onChange(e.currentTarget.value)}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      {hint && <p class="field-hint">{hint}</p>}
    </div>
  )
}

// Выбор одного из немногих крупными карточками: путь мастера, «дома / в
// машине». Радиогруппа, а не select: варианты надо прочитать, а не вспомнить.
export function ChoiceList({ label, value, options = [], onChange }) {
  return (
    <div class="choice-list" role="radiogroup" aria-label={label}>
      {options.map((o) => {
        const on = o.value === value
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={on ? 'true' : 'false'}
            class={`choice${on ? ' choice-on' : ''}`}
            onClick={() => onChange(o.value)}
          >
            <span class="choice-mark" aria-hidden="true" />
            <span class="choice-text">
              <span class="choice-title">{o.title}</span>
              {o.sub && <span class="choice-sub">{o.sub}</span>}
            </span>
          </button>
        )
      })}
    </div>
  )
}
