// Секция экрана: надзаголовок плюс содержимое. Отдельный компонент нужен,
// чтобы отступы и типографика надзаголовков не расползались по экранам.
export function Section({ title, action, children }) {
  return (
    <section class="section">
      {(title || action) && (
        <div class="section-head">
          {title && <h2 class="section-title">{title}</h2>}
          {action}
        </div>
      )}
      {children}
    </section>
  )
}
