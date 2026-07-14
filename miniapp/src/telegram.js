const tg = window.Telegram?.WebApp

export function initTelegram() {
  if (!tg) return
  tg.ready()
  tg.expand()
}

export function getInitData() {
  return tg?.initData ?? ''
}

export function getThemeParams() {
  return tg?.themeParams ?? {}
}

export function onBackButtonClick(handler) {
  if (!tg) return () => {}
  tg.BackButton.onClick(handler)
  return () => tg.BackButton.offClick(handler)
}

export function onThemeChanged(handler) {
  if (!tg) return () => {}
  tg.onEvent('themeChanged', handler)
  return () => tg.offEvent('themeChanged', handler)
}

export function setBackButtonVisible(visible) {
  if (!tg) return
  if (visible) tg.BackButton.show()
  else tg.BackButton.hide()
}
