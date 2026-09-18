// Подсветка обслуживания на главном экране роутера (оператор 18.09): нужна
// перезагрузка или вышло обновление -- видно всем, кто открыл роутер, а не
// только тому, кто дошёл до «Управления». Источник -- тот же ответ /versions,
// что у раздела «Обновления»: отложенные новости сервер сюда не присылает.
function line(name, available, installed) {
  return installed ? `${name}: ${available} (сейчас ${installed})` : `${name}: ${available}`
}

export function maintenanceNotice(versions) {
  if (!versions) return null
  const lines = (versions.rows ?? []).filter((r) => r?.available).map((r) => line(r.name || r.component, r.available, r.installed))
  if (versions.agent?.available) lines.push(line('Агент wg-monitor', versions.agent.available, versions.agent.installed))
  const reboot = Boolean(versions.reboot_hint)
  if (!reboot && lines.length === 0) return null
  return {
    tone: reboot ? 'warn' : 'info',
    title: reboot ? 'Нужна перезагрузка роутера' : 'Есть обновления',
    note: reboot ? 'Сменился модуль ядра AmneziaWG — новый заработает после перезагрузки.' : '',
    lines,
  }
}
