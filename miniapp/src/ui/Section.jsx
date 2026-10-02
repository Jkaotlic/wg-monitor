import { createContext } from 'preact'
import { useContext } from 'preact/hooks'

// Уровень заголовка раздела. Сам по себе раздел -- h2; внутри группы с
// собственным h2 («Проверки»: группы; «Настройки»: свёртки) -- h3, иначе у
// скринридера h2 лежал бы в h2. Вид задают классы, а не тег.
export const HeadingLevel = createContext(2)

// Группа с заголовком h2: всё внутри неё -- на уровень глубже.
export function HeadingGroup({ children }) {
  return <HeadingLevel.Provider value={3}>{children}</HeadingLevel.Provider>
}

// Заголовок раздела нужного уровня; deeper -- подзаголовок внутри раздела.
export function SectionHeading({ class: cls = 'section-title', deeper = false, children }) {
  const level = Math.min(6, useContext(HeadingLevel) + (deeper ? 1 : 0))
  const Tag = `h${level}`
  return <Tag class={cls}>{children}</Tag>
}

// Секция экрана: надзаголовок плюс содержимое. Отдельный компонент нужен,
// чтобы отступы и типографика надзаголовков не расползались по экранам.
export function Section({ title, action, children }) {
  return (
    <section class="section">
      {(title || action) && (
        <div class="section-head">
          {title && <SectionHeading>{title}</SectionHeading>}
          {action}
        </div>
      )}
      {children}
    </section>
  )
}
