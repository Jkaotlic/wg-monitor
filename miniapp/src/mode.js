// Режим приложения решается по адресу, а не по наличию Telegram-SDK и не по
// платформе: /dashboard/* бэкенд отдаёт без скрипта Telegram, и признак
// «где открыли» обязан быть детерминированным -- одинаковым в браузере, в
// песочнице и в тестах.
export function appMode(pathname) {
  return typeof pathname === 'string' && pathname.startsWith('/dashboard') ? 'web' : 'telegram'
}

// Ширина -- единственный признак раскладки: и браузер, и развёрнутый Telegram
// Desktop получают широкую, телефон -- нынешнюю.
export const WIDE_QUERY = '(min-width: 1024px)'
