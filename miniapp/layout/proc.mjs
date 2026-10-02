// Остановка песочницы: SIGTERM группе процесса и ОЖИДАНИЕ выхода (не пауза на
// глаз) -- порт свободен ровно тогда, когда процесс умер. Глухого к SIGTERM
// добивает SIGKILL по истечении graceMs.
function signal(child, sig) {
  for (const fn of [() => process.kill(-child.pid, sig), () => child.kill(sig)]) {
    try {
      fn()
      return
    } catch {
      // ESRCH или pid не число -- уже мёртв; пробуем следующий способ
    }
  }
}

// Без ожидания -- для обработчика выхода самого скрипта.
export function killChild(child) {
  signal(child, 'SIGTERM')
}

export function stopChild(child, { graceMs = 5000 } = {}) {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve()
  return new Promise((resolve) => {
    const timer = setTimeout(() => signal(child, 'SIGKILL'), graceMs)
    child.once('exit', () => {
      clearTimeout(timer)
      resolve()
    })
    signal(child, 'SIGTERM')
  })
}
