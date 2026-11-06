import { Component } from 'preact'
import { Suspense } from 'preact/compat'

class LazyScreenBoundary extends Component {
  constructor(props) {
    super(props)
    this.state = { failed: false }
  }

  componentDidCatch() {
    this.setState({ failed: true })
  }

  render(props, state) {
    if (state.failed) {
      return (
        <div class="screen">
          <p class="state state-error">Не удалось загрузить экран. Соединение могло оборваться во время обновления приложения.</p>
          <button type="button" class="btn btn-ghost btn-wide" onClick={() => window.location.reload()}>Перезагрузить</button>
        </div>
      )
    }
    return props.children
  }
}

export function LazyScreen({ children }) {
  return (
    <LazyScreenBoundary>
      <Suspense fallback={<p class="state">Загрузка экрана…</p>}>
        {children}
      </Suspense>
    </LazyScreenBoundary>
  )
}
