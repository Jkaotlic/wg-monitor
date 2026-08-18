const tg = window.Telegram?.WebApp

export function initTelegram() {
  if (!tg) return
  tg.ready()
  tg.expand()
}

export function getInitData() {
  return tg?.initData ?? ''
}

// Тему выбирает Telegram, цвета -- приложение (см. theme.js). Вне Telegram
// (локальная отладка в браузере) схему подсказывает сама система.
export function getColorScheme() {
  if (tg?.colorScheme) return tg.colorScheme
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

// Telegram рисует свою шапку и фон вокруг вебвью. Если их не покрасить,
// приложение выглядит вклеенным в чужое окно.
export function paintChrome(palette) {
  if (!tg) return
  tg.setHeaderColor?.(palette.page)
  tg.setBackgroundColor?.(palette.page)
}

export function onBackButtonClick(handler) {
  if (!tg) return () => {}
  tg.BackButton.onClick(handler)
  return () => tg.BackButton.offClick(handler)
}

export function onColorSchemeChanged(handler) {
  if (!tg) {
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)')
    if (!mq) return () => {}
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }
  tg.onEvent('themeChanged', handler)
  return () => tg.offEvent('themeChanged', handler)
}

export function setBackButtonVisible(visible) {
  if (!tg) return
  if (visible) tg.BackButton.show()
  else tg.BackButton.hide()
}
