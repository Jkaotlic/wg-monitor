// Новости об обновлениях и снимок версий роутера.
//
// Экран больше не зависит от нажатия: версии приезжают из базы срезом
// /routers/{id}/versions, а кнопка «Сверить версии сейчас» осталась как
// «перепроверить», а не как единственный способ что-то узнать.
//
// Главное правило этого файла: ОТСУТСТВИЕ СВЕДЕНИЙ И ОТРИЦАТЕЛЬНЫЙ ОТВЕТ
// ЗВУЧАТ ПО-РАЗНОМУ. «Мы не знаем, вышло ли обновление» -- не «обновлений
// нет»; «про HydraRoute сведений нет» -- не «HydraRoute не установлен».
// Раньше все эти случаи выглядели одинаково: блока на экране просто не было.

// Последствие обновления панели. Знает его и клиент: старый бэкенд подсказку
// не пришлёт, а молчать об уроненных VPN-туннелях экран не имеет права.
const AWGMGR_CONSEQUENCE =
  'Обновление может сменить модуль ядра, и VPN-туннели поднимутся только после перезагрузки роутера.'

// Как называется компонент в предложении «Вышла X «версия»».
const NOUN = {
  awgmgr: 'awg-manager',
  hrneo: 'HydraRoute Neo',
  firmware: 'прошивка',
}

// Заголовок строки на экране -- по-русски и о роли, а не о пакете.
const TITLE = {
  awgmgr: 'Панель роутера',
  hrneo: 'Обход блокировок',
  firmware: 'Прошивка роутера',
}

// Причины «неизвестно» -- закрытый список (upstream.Reason* на бэкенде).
// Незнакомая причина не показывается вовсе: код причины человеку ничего не
// говорит, а выдумывать за него текст нельзя.
export function unknownLine(reason, checkedAgo) {
  switch (reason) {
    case 'upstream_unavailable':
      return checkedAgo
        ? `Проверить обновления не удалось, последний раз смотрели ${checkedAgo}.`
        : 'Проверить обновления не удалось.'
    case 'upstream_not_configured':
      return 'Проверка обновлений не настроена — мы не знаем, что вышло.'
    case 'no_snapshot':
      return 'Роутер ещё не рассказал про версии.'
    case 'agent_too_old':
      return 'Агент на роутере старый и про модуль ядра не сообщает.'
    default:
      return ''
  }
}

// Новости: что вышло, что стоит и чем это грозит.
//
// Действия рядом с новостью нет ни одного (row.action не появляется): точечно
// обновить пакет агент не умеет, валовый opkg_upgrade не отвечает на фразу
// «вышел awg-manager», а кнопка без бэкенда не рисуется.
export function versionsRows(payload) {
  const rows = payload?.rows ?? []
  return rows.map((r) => {
    const noun = NOUN[r.component] ?? r.name ?? r.component
    let text = `Вышла ${noun} «${r.available}», на роутере «${r.installed}».`
    const hint = r.hint || (r.component === 'awgmgr' ? AWGMGR_CONSEQUENCE : '')
    if (hint) text += ` ${hint}`
    return {
      key: r.component,
      component: r.component,
      title: TITLE[r.component] ?? r.name ?? r.component,
      code: r.component === 'firmware' ? 'KeeneticOS' : r.name,
      value: r.available,
      text,
      tone: 'warn',
      available: r.available,
    }
  })
}

// Что стоит на роутере по последнему снимку.
//
// Про HydraRoute и про модуль ядра сервер отвечает указателем: отсутствие
// ключа означает «опрос не дал ответа». Писать по такому молчанию «не
// установлен» запрещено -- у владельца, у которого HydraRoute стоит и
// работает, это было бы прямое враньё (разведка 12.09.2026: стоит на всех
// проверенных роутерах).
export function installedRows(payload) {
  const inst = payload?.installed
  if (!inst) return []
  const rows = []
  rows.push({
    key: 'awgmgr',
    title: 'Панель роутера',
    code: 'awg-manager',
    value: inst.awgmgr || 'сведений нет',
    tone: inst.awgmgr ? 'ok' : 'muted',
  })
  rows.push({
    key: 'hrneo',
    title: 'Обход блокировок',
    code: 'HydraRoute Neo',
    value: hrneoValue(inst),
    tone: inst.hrneo || inst.hrneo_installed === true ? 'ok' : 'muted',
  })
  if (inst.firmware) {
    rows.push({
      key: 'firmware',
      title: 'Прошивка роутера',
      code: 'KeeneticOS',
      value: inst.firmware,
      tone: 'ok',
    })
  }
  if (inst.keenetic_os) {
    rows.push({ key: 'model', title: 'Модель роутера', value: inst.keenetic_os, tone: 'ok' })
  }
  rows.push({
    key: 'kmod',
    title: 'Модуль ядра',
    code: 'AmneziaWG',
    value: inst.kmod || 'сведений нет',
    // false приходит только тогда, когда агент правда ответил «не загружен», --
    // а это уже поломка, а не незнание.
    valueSub: inst.kmod_loaded === false ? 'не загружен' : '',
    tone: inst.kmod_loaded === false ? 'danger' : inst.kmod ? 'ok' : 'muted',
  })
  return rows
}

function hrneoValue(inst) {
  if (inst.hrneo) return inst.hrneo
  if (inst.hrneo_installed === true) return 'установлен'
  if (inst.hrneo_installed === false) return 'не установлен'
  return 'сведений нет'
}
