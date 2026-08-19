// Описание шита -- данные, а не JSX: заводят его экраны, показывает оболочка,
// и между ними ходит один объект. Само выполнение команды живёт внутри
// компонента Sheet, поэтому здесь нет ни busy, ни result.
export function confirmSheet({ routerID, title, body, action, args = {}, buttonLabel = 'Выполнить', danger = false, asleep = false, onDone }) {
  return { routerID, title, body, action, args, buttonLabel, danger, asleep, onDone }
}

// Фаза выводится из состояния useCommand, а не хранится отдельно: два
// источника правды разъехались бы на первой же ошибке.
export function sheetPhase({ busy, result, error } = {}) {
  if (busy) return 'running'
  if (error) return 'error'
  if (result) return 'done'
  return 'confirm'
}
