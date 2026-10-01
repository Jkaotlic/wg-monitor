import { Overlay } from '../ui/Overlay.jsx'
import { PackagesCard } from './PackagesCard.jsx'

// «Пакеты по расписанию» -- экран Обслуживания (спека, п. 9): обновление
// пакетов Entware по cron и очистка Entware. Только админ: вход рисуется
// только ему, сервер отвечает остальным 404.
export function PackagesScreen({ routerID, routerName, asleep, onClose }) {
  return (
    <Overlay title="Пакеты по расписанию" backLabel="Настройки" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">Пакеты по расписанию</h1>
        {routerName && <p class="router-lastseen">{routerName}</p>}
        {asleep && <p class="hint">Роутер сейчас не на связи — команды подождут его несколько минут.</p>}
        <PackagesCard kind="opkg" routerID={routerID} asleep={asleep} />
        <PackagesCard kind="clean" routerID={routerID} asleep={asleep} />
      </div>
    </Overlay>
  )
}
