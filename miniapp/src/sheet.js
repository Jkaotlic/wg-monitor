import { plainHyphens } from './text.js'

// Описание шита -- данные, а не JSX: заводят его экраны, показывает оболочка,
// и между ними ходит один объект. Само выполнение команды живёт внутри
// компонента Sheet, поэтому здесь нет ни busy, ни result.
export function confirmSheet({ routerID, title, body, action, args = {}, buttonLabel = 'Выполнить', danger = false, asleep = false, confirmPhrase = '', onDone }) {
  return { routerID, title, body, action, args, buttonLabel, danger, asleep, confirmPhrase, onDone }
}

// Подтверждение действия, которое человек делает СЕБЕ, а не роутеру:
// выключение уведомлений живёт в бэкенде, команду агенту слать незачем.
// Оформление берётся то же самое -- чтобы «точно?» везде выглядело одинаково.
export function localSheet({ title, body, buttonLabel = 'Выполнить', danger = false, perform, onDone }) {
  return { title, body, buttonLabel, danger, perform, onDone, args: {} }
}

// Необратимое действие подтверждается набором, а не нажатием: человек
// печатает имя роутера, и только совпадение включает кнопку. Смысл не в
// защите от чужого пальца, а в паузе -- это единственное место, где он
// читает, что именно произойдёт, до того как это произойдёт.
//
// Сравнение по сути: регистр, пробелы по краям и вид дефиса человек
// воспроизводит случайно, и отказ из-за них учил бы только злости. Дефис
// приводится с обеих сторон: имя роутера, скопированное из текста, может
// нести U+2011, а набранное руками -- обычный.
export function confirmReady(sheet, typed) {
  const phrase = plainHyphens(sheet?.confirmPhrase ?? '').trim().toLowerCase()
  if (!phrase) return true
  return plainHyphens(typed).trim().toLowerCase() === phrase
}

// Фаза выводится из состояния useCommand, а не хранится отдельно: два
// источника правды разъехались бы на первой же ошибке.
export function sheetPhase({ busy, result, error } = {}) {
  if (busy) return 'running'
  if (error) return 'error'
  if (result) return 'done'
  return 'confirm'
}
