import { SegmentTabs } from '../ui/SegmentTabs.jsx'
import { DiagTab } from './DiagTab.jsx'
import { EventsTab } from './EventsTab.jsx'

// «Проверки» (v0.52): сверху -- «Сейчас | Что было». «Что было» -- прежняя
// вкладка «Что было» без изменений содержания; ссылка ?tab=events открывает её.
export const CHECKS_VIEWS = [
  { id: 'now', title: 'Сейчас' },
  { id: 'history', title: 'Что было' },
]

export function ChecksTab({ routerID, routerName, asleep, isAdmin = false, openSheet, view = 'now', onView }) {
  const current = view === 'history' ? 'history' : 'now'
  return (
    <div class="checks-tab">
      <div class="checks-segment">
        <SegmentTabs label="Проверки" tabs={CHECKS_VIEWS} value={current} onChange={(id) => onView?.(id)} />
      </div>
      {current === 'history' ? (
        <EventsTab routerID={routerID} routerName={routerName} />
      ) : (
        <DiagTab routerID={routerID} asleep={asleep} isAdmin={isAdmin} openSheet={openSheet} />
      )}
    </div>
  )
}
