import { createContext } from 'preact'

// Режим (web/telegram) и раскладка (wide) нужны глубоко внутри экранов --
// «Открыть в браузере» в Парке, две колонки «Сейчас». Прокидывать их через
// пять уровней свойств значило бы трогать каждый экран по пути.
export const AppContext = createContext({ mode: 'telegram', wide: false })
