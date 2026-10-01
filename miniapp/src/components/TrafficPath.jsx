import { pathState } from '../trafficPath.js'

// Схема пути трафика: устройства → роутер → развилка.
//
// Заменяет прибор с портами DNS/EXT/HR/AWGM/AGEN. Тот рисовал внутренние
// проверки как разъёмы железки и требовал легенду под собой — если картинке
// нужна инструкция, она не работает. Эта показывает то, ради чего сервис
// существует: заблокированное идёт через туннель, остальное напрямую.
//
// Ветки живут порознь. Упавший туннель рвётся пунктиром, а прямая ветка
// остаётся целой — человек видит, что банки и госуслуги у него работают, и не
// решает, что интернет пропал целиком.
//
// v0.52: подписи -- обычный текст страницы, а не текст внутри масштабируемого
// SVG. Прежняя схема рисовалась под 342 px и сжималась вместе с карточкой: на
// 360 px «ЧЕРЕЗ VPN-ТУННЕЛЬ» выходил в 8,6 px. Теперь размер шрифта -- из шкалы
// приложения (не мельче 12 px на любой ширине), длинное имя VPN-туннеля
// переносится, а SVG рисует только развилку (линии тянутся, толщина -- нет).
const TUNNEL_CAPTION = {
  up: 'то, что заблокировано',
  down: 'не отвечает',
  unknown: 'роутер не сказал',
}

export function TrafficPath({ traffic, incidents, tunnels, stale }) {
  const s = pathState({ traffic, incidents, tunnels, stale })
  const viaLabel = s.via || 'VPN-туннель'

  return (
    <div
      class="traffic-path"
      role="img"
      aria-label={`Схема: заблокированное идёт через ${s.via ? `VPN-туннель «${s.via}»` : 'VPN-туннель'}, остальное напрямую`}
    >
      <div class="tp-node tp-devices">
        <svg viewBox="0 0 50 18" width="50" height="18" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" aria-hidden="true">
          <rect x="1" y="1.5" width="10" height="15" rx="2" />
          <rect x="17" y="3.5" width="15" height="11" rx="1.6" />
          <path d="M15 16.5h19" />
          <circle cx="43" cy="9" r="6" />
        </svg>
        <span>Ваши устройства</span>
      </div>
      <span class="tp-stem" />
      <div class={`tp-node tp-router tp-router-${s.tunnel}`}>
        <span class={`tp-dot${stale ? ' tp-dot-stale' : ''}`} />
        <span>Роутер</span>
      </div>

      {/* Развилка: левая ветка рвётся пунктиром, когда VPN-туннель не отвечает.
          Концы линий -- над серединами карточек веток (четверть и три четверти). */}
      <svg class="tp-fork" viewBox="0 0 342 36" preserveAspectRatio="none" fill="none" aria-hidden="true">
        <path class={`tp-line tp-line-${s.tunnel}`} d="M171 0v8c0 8-7 10-14 10H95c-8 0-12 4-12 12v6" />
        <path class={`tp-line tp-line-${s.direct}`} d="M171 0v8c0 8 7 10 14 10h62c8 0 12 4 12 12v6" />
      </svg>

      <div class="tp-branches">
        <div class={`tp-branch tp-branch-${s.tunnel}`}>
          <span class="tp-kicker">{s.tunnel === 'down' ? 'VPN-ТУННЕЛЬ МОЛЧИТ' : 'ЧЕРЕЗ VPN-ТУННЕЛЬ'}</span>
          <span class="tp-title">{viaLabel}</span>
          <span class="tp-note">{TUNNEL_CAPTION[s.tunnel]}</span>
        </div>
        <div class={`tp-branch tp-branch-direct${stale ? ' tp-branch-stale' : ''}`}>
          <span class="tp-kicker">{stale ? 'НЕИЗВЕСТНО' : 'НАПРЯМУЮ'}</span>
          <span class="tp-title">Без VPN-туннеля</span>
          <span class="tp-note">банки, госуслуги, ТВ</span>
        </div>
      </div>
    </div>
  )
}
