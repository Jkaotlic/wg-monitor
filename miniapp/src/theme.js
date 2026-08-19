// Палитра приложения. Единственный источник истины по цвету.
//
// Раньше цвета выводились из themeParams Telegram, и каждый компонент
// вынужден был защищаться color-mix'ами, чтобы удержать контраст на чужой
// палитре. Теперь у приложения своя палитра (из макета), а от Telegram
// берётся только выбор светлая/тёмная. Числа контраста проверяет
// test/theme.test.js -- менять цвет, не прогнав тест, нельзя.
//
// Светлый набор -- из макета, с одной правкой: макет красит надзаголовки
// секций в #8a8a8e, что на фоне страницы даёт 2.84:1 при требуемых 4.5.
// Заменено на #67676c -- шесть пунктов темнее, глазом неотличимо, проходит.
//
// Тёмный набор выведен из палитры, которой уже нарисован корпус роутера в
// RouterDevice.jsx (#15181d, #252a30, #7b8391), чтобы тема и устройство не
// спорили друг с другом.
export const PALETTES = {
  light: {
    page: '#e9e9ef',
    surface: '#f2f2f7',
    text: '#1c1c1e',
    muted: '#67676c',
    accent: '#226398',
    accentFill: '#175485',
    ok: '#1E6F46',
    warn: '#8a5a00',
    danger: '#A93B3F',
    border: 'rgba(28,28,30,0.12)',
    shadowCard: '0 1px 2px rgba(28,28,30,0.07)',
  },
  dark: {
    page: '#15181d',
    surface: '#21262d',
    text: '#e6e9ee',
    muted: '#a2abb8',
    accent: '#7fb6e6',
    accentFill: '#2b6ea8',
    ok: '#3fbf7a',
    warn: '#f0b429',
    danger: '#ff8a86',
    border: 'rgba(255,255,255,0.14)',
    // На тёмном фоне тень читается как грязь; уровень задаёт сам surface.
    shadowCard: 'none',
  },
}

export function cssVarName(key) {
  return '--' + key.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())
}

// Применяет палитру к документу: пишет переменные и ставит data-theme, по
// которому те же значения объявлены в style.css (дефолт до загрузки JS).
// root передаётся параметром, чтобы тест мог подсунуть заглушку вместо
// настоящего document.
export function applyPalette(scheme, root = document.documentElement) {
  const name = scheme === 'dark' ? 'dark' : 'light'
  const palette = PALETTES[name]
  for (const [key, value] of Object.entries(palette)) {
    root.style.setProperty(cssVarName(key), value)
  }
  root.setAttribute('data-theme', name)
  return palette
}
