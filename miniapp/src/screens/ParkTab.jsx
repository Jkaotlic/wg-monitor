import { fleetSummary } from '../fleet.js'
import { FleetCounts } from '../ui/FleetCounts.jsx'
import { ParkSection } from './ParkSection.jsx'

// Вкладка «Парк» (v0.48) -- всё, что админ делает с парком целиком, одним
// экраном: сводка (три числа, бэкенд и его обновление), действия над всеми
// роутерами, роутеры с их кнопками, а в конце -- добавить роутер, свои
// VPN-серверы и вход из браузера. Раньше это жило хвостом под «Моими
// роутерами», и список роутеров превращался в экран инструментов.
//
// Сам Парк -- ParkSection, перенесённый, а не переписанный: листы, слои и
// их возвраты прежние, возврат теперь -- в эту вкладку (returnTo 'park').
export function ParkTab({ routers, onPick, openSheet, openLayer, onOpenConnection }) {
  return (
    <div class="screen park-tab">
      <h1 class="screen-title">Парк</h1>
      <FleetCounts summary={fleetSummary(routers)} />
      <ParkSection routers={routers} openSheet={openSheet} onOpenRouter={onPick} currentID={null} openLayer={openLayer} onOpenConnection={onOpenConnection} />
    </div>
  )
}
