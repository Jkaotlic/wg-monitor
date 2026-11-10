import { localSheet } from './sheet.js'
import { recoverRouterEntware, fetchEntwareRecoveryScript } from './api.js'

export function entwareRecoveryAvailable(settings) {
  return settings?.role === 'admin'
}

export function entwareRecoverySheet({ routerID, onDone, start = recoverRouterEntware }) {
  return localSheet({
    title: 'Восстановить команды Entware?',
    body: 'Проверим BusyBox и блокировки обновления, поставим BusyBox на hold и восстановим отсутствующие ссылки на команды. Все существующие файлы и ссылки сохранятся. Пакеты не переустанавливаются, VPN не перезапускается. Нужен сохранённый вход в awg-manager с Expert; рабочий агент не требуется. При ошибке hold или часть ссылок могут сохраниться.',
    buttonLabel: 'Восстановить и защитить',
    busyLabel: 'Запускаем восстановление…',
    perform: () => start(routerID),
    onDone,
    errorText: (err) => err?.serverMessage || 'Не удалось начать восстановление. Проверьте сохранённый вход в awg-manager или скачайте скрипт для ручного запуска.',
  })
}

export async function downloadEntwareRecoveryScript() {
  const data = await fetchEntwareRecoveryScript()
  if (typeof data?.content !== 'string' || !data.content.startsWith('#!/opt/bin/busybox sh\n')) throw new Error('invalid recovery script')
  const blob = new Blob([data.content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = 'entware-recover.run'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  return data.instructions
}
