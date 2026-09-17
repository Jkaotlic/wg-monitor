import { plainHyphens } from './text.js'

// Описание шита -- данные, а не JSX: заводят его экраны, показывает оболочка,
// и между ними ходит один объект. Само выполнение команды живёт внутри
// компонента Sheet, поэтому здесь нет ни busy, ни result.
// commandLabel -- как назвать команду на листе словами человека. Без него лист
// покажет имя действия агента, а внутренних имён владельцу показывать нельзя.
export function confirmSheet({ routerID, title, body, action, args = {}, buttonLabel = 'Выполнить', danger = false, asleep = false, confirmPhrase = '', commandLabel = '', onDone, onResult }) {
  return { routerID, title, body, action, args, buttonLabel, danger, asleep, confirmPhrase, commandLabel, onDone, onResult }
}

// Подтверждение действия, которое выполняет бэкенд, а не роутер: выключение
// уведомлений, обновление агента, отмена обновления. Оформление то же самое --
// чтобы «точно?» везде выглядело одинаково.
//
// confirmPhrase -- как у командного листа: необратимое подтверждается
// набором. perform получает набранное (сервер сверяет его сам), onDone --
// ответ сервера. errorText(err) -- фраза экрана для отказа по коду; пустая
// строка значит «своей фразы нет», и лист скажет общее «не получилось».
// busyLabel -- подпись кнопки, пока perform выполняется («Ставим…» для
// обновления агента, «Сохраняем…» для выключения уведомлений). Пустая строка
// значит «своей подписи нет» -- Sheet.jsx покажет прежнюю по умолчанию.
//
// fields -- поля формы листа (пароль, логин, срок). Значения полей живут
// только в состоянии Sheet.jsx: описание листа лежит в состоянии App, и
// введённый пароль не должен туда попасть. perform получает их вторым
// аргументом -- снимком на момент нажатия. fieldsReady(values) -- когда
// кнопка может загореться; note -- предупреждение под полями.
// Поле может быть переключателем (type: 'toggle', значение boolean), может
// появляться по условию (showIf(values)) и нести подсказку, которая следует
// вводу (hint(values)): «Это откат: на роутере v0.35.0».
//
// confirmStrict -- набор сверяется строго, как на сервере (только пробелы по
// краям): раскатка бэкенда сравнивает версию без поблажек регистра и дефиса.
export function localSheet({ title, body, buttonLabel = 'Выполнить', danger = false, confirmPhrase = '', confirmStrict = false, errorText, busyLabel = '', perform, onDone, fields = [], fieldsReady, note = '' }) {
  return { title, body, buttonLabel, danger, confirmPhrase, confirmStrict, errorText, busyLabel, perform, onDone, fields, fieldsReady, note, args: {} }
}

export function initialFieldValues(fields) {
  const values = {}
  for (const f of fields ?? []) values[f.name] = f.initial ?? (f.type === 'toggle' ? false : '')
  return values
}

// Значения после отправки: всё стирается (пароли), кроме полей с keep: true --
// несекретного ввода вроде версии, который после отказа сервера перенабирать
// незачем.
export function keptFieldValues(fields, values) {
  const fresh = initialFieldValues(fields)
  for (const f of fields ?? []) if (f.keep === true && f.type !== 'password') fresh[f.name] = values?.[f.name] ?? fresh[f.name]
  return fresh
}

export function fieldsReady(sheet, values) {
  return typeof sheet?.fieldsReady === 'function' ? Boolean(sheet.fieldsReady(values ?? {})) : true
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
  if (sheet?.confirmStrict) {
    const exact = String(sheet.confirmPhrase ?? '').trim()
    return !exact || String(typed ?? '').trim() === exact
  }
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
