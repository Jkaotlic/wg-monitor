import { Overlay } from '../ui/Overlay.jsx'
import { AccessSection } from './AccessSection.jsx'

// Администрирование. В этой фазе здесь только доступы: обслуживание, бэкапы и
// подключение новых роутеров живут в браузерном дашборде и приедут сюда
// отдельными фазами. Пустые пункты-заглушки не рисуем -- кнопка, которая
// ничего не делает, хуже её отсутствия.
export function AdminOverlay({ routerID, onClose }) {
  return (
    <Overlay title="Обслуживание и доступы" backLabel="Роутер" onBack={onClose}>
      <div class="screen">
        <AccessSection routerID={routerID} />
        <p class="muted admin-note">
          Обслуживание пакетов, бэкапы и подключение новых роутеров пока живут в браузерном
          дашборде.
        </p>
      </div>
    </Overlay>
  )
}
