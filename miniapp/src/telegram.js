const tg = window.Telegram?.WebApp

// Скрипт telegram-web-app.js подключается всегда и вне Telegram тоже: он
// создаёт объект WebApp с colorScheme = 'light' и platform = 'unknown'.
// Поэтому "мы внутри Telegram" -- это платформа, а не наличие объекта; иначе
// локальная отладка всегда светлая, каким бы ни было оформление системы.
const inTelegram = Boolean(tg && tg.platform && tg.platform !== 'unknown')

export function initTelegram() {
  if (!tg) return
  tg.ready()
  tg.expand()
}

export function getInitData() {
  return tg?.initData ?? ''
}

// Telegram рисует свою шапку и фон вокруг вебвью. Если их не покрасить,
// приложение выглядит вклеенным в чужое окно.
export function paintChrome(palette) {
  if (!tg) return
  tg.setHeaderColor?.(palette.bg)
  tg.setBackgroundColor?.(palette.bg)
}

export function onBackButtonClick(handler) {
  if (!tg) return () => {}
  tg.BackButton.onClick(handler)
  return () => tg.BackButton.offClick(handler)
}

export function setBackButtonVisible(visible) {
  if (!tg) return
  if (visible) tg.BackButton.show()
  else tg.BackButton.hide()
}
