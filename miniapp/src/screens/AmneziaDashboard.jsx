import { Section } from '../ui/Section.jsx'
import { openExternal } from '../telegram.js'

export function AmneziaDashboard() {
  return (
    <Section title="Amnezia Premium">
      <p class="hint">Войдите на сайте Amnezia и выберите страну в разделе «Файлы конфигурации». Для скачанного файла нужен роутер с поддержкой AmneziaWG 3.1.</p>
      <button type="button" class="btn btn-primary btn-wide" onClick={() => openExternal('https://cp.amnezia.org/ru')}>
        Открыть личный кабинет Amnezia ↗
      </button>
    </Section>
  )
}
