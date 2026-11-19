const REASON = {
  command_timeout: ['Роутер не ответил вовремя', 'danger'],
  command_locked: ['Другая операция ещё не закончилась', 'warn'],
  enqueue_failed: ['Не удалось передать команду', 'danger'],
  command_failed: ['Операция завершилась с ошибкой', 'danger'],
}

export function operationView(operation = {}) {
  let status = 'Результат не подтверждён'
  let tone = 'muted'
  if (operation.outcome === 'ok') {
    status = 'Завершено'
    tone = 'ok'
  } else if (operation.outcome === 'not_delivered') {
    status = 'Команда не была выдана роутеру'
    tone = operation.action === 'agent_update' ? 'muted' : 'warn'
  } else if (operation.outcome === 'failed') {
    ;[status, tone] = REASON[operation.reason_code] ?? ['Не завершено', 'danger']
  }
  return {
    title: operation.summary || 'Операция на роутере',
    status,
    tone,
    at: operation.finished_at || operation.started_at || '',
  }
}
