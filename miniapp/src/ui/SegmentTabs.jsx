// Вкладки внутри экрана (кабинеты: Amnezia · HideMy · Свой сервер). Не путать
// с TabBar -- нижней навигацией приложения: там место, здесь -- вид данных
// одного экрана, и в адрес выбор не пишется.
export function SegmentTabs({ label, tabs = [], value, onChange }) {
  return (
    <div class="segment-tabs" role="tablist" aria-label={label}>
      {tabs.map((t) => {
        const on = t.id === value
        return (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={on ? 'true' : 'false'}
            class={`segment-tab${on ? ' segment-tab-on' : ''}`}
            onClick={() => onChange(t.id)}
          >
            {t.title}
          </button>
        )
      })}
    </div>
  )
}
