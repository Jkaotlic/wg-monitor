// Давность показаний роутера -- одно правило на всё приложение.
//
// Статус роутера отвечает на вопрос «есть ли тревога», а не «на связи ли он»:
// пока открыта тревога, сервер пишет «alert», сколько бы роутер ни молчал
// (dashboard_handler.go, контракт статуса). 27.09 так роутер с тревогой молчал
// 47 часов, а экран описывал его поломку в настоящем времени. С v0.46 сервер
// отдаёт отдельный признак `stale` -- отчёт старше тех же порогов, что делают
// роутер offline/sleeping, независимо от тревог.
//
// Старый бэкенд поля не шлёт. Пороги давности в списке роутеров не приходят
// (только в настройках одного роутера), поэтому угадывать их здесь нельзя:
// без поля устаревшим считается только offline/sleeping, как раньше.
// С v0.46 сервер отдаёт и reach ("online"|"sleeping"|"offline") -- связь,
// посчитанную без учёта тревог (review v0.46, п. 4). Когда он есть, он
// единственный источник: и подпись, и фильтр, и давность берутся из него.
const REACH = new Set(['online', 'sleeping', 'offline'])

function serverReach(router) {
  return REACH.has(router?.reach) ? router.reach : ''
}

export function isStale(router) {
  if (!router) return false
  const reach = serverReach(router)
  if (reach) return reach !== 'online'
  if (router.stale === true) return true
  return router.status === 'offline' || router.status === 'sleeping'
}

// Статус для списка и фильтров: молчащая тревога показывается как молчание.
// Без reach у мобильного сервер между «спит» и «выключен» различает по
// порогам, которых здесь нет, -- берём мягкое «спит»: ложное «выключен»
// пугало бы зря, а «спит» у молчащего мобильного правда в любом случае.
export function reachStatus(router) {
  const status = router?.status
  if (status !== 'alert') return status
  const reach = serverReach(router)
  if (reach) return reach === 'online' ? 'alert' : reach
  if (router?.stale === true) return router?.kind === 'mobile' ? 'sleeping' : 'offline'
  return status
}
