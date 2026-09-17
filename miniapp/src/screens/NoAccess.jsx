import { useState } from 'preact/hooks'
import { fetchRouters } from '../api.js'
import { CopyButton } from '../ui/CopyButton.jsx'

// Первый вход человека, которому ещё не выдали доступ. Пустой доступ -- это
// состояние системы, а не ошибка приложения, и говорить о нём надо прямо,
// вместе с тем, что делать.
//
// Шаг ровно один и он настоящий: администратору нужен номер этого человека в
// Telegram, а сам он его нигде не видит. Номер приходит из сессии (её отдаёт
// /v1/miniapp/session полем telegram_user_id) -- показываем его кнопкой
// «Скопировать» рядом. Прежний шаг «нажмите кнопку в теме своего роутера»
// исчез вместе с темами группы (цикл 5): нажимать больше негде.
export function NoAccess({ telegramUserID = 0, onRetry }) {
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState(false)

  function recheck() {
    setChecking(true)
    fetchRouters()
      .then((data) => {
        if ((data.routers ?? []).length > 0 && onRetry) {
          onRetry(data.routers)
          return
        }
        setChecked(true)
      })
      .catch(() => setChecked(true))
      .finally(() => setChecking(false))
  }

  return (
    <div class="screen">
      <h1 class="screen-title">Роутер ещё не привязан</h1>
      <p class="router-lastseen">
        Приложение открывается, но показывать пока нечего: ваш Telegram не связан ни с одним
        роутером.
      </p>

      <section class="section">
        <h2 class="section-title">Что сделать</h2>
        <div class="card">
          {telegramUserID > 0 ? (
            <div class="noaccess-id">
              <p class="noaccess-id-label">Передайте администратору ваш Telegram ID:</p>
              <p class="noaccess-id-row">
                <b class="noaccess-id-value">{telegramUserID}</b>
                <CopyButton text={String(telegramUserID)} />
              </p>
            </div>
          ) : (
            <p class="noaccess-id">
              Передайте администратору своё имя в Telegram — он выдаст доступ к роутеру.
            </p>
          )}
          <p class="card-foot">
            По этому номеру он добавит вас владельцем или оператором роутера на экране
            «Доступ». <b>Пароль от роутера приложение не спрашивает никогда.</b>
          </p>
        </div>
      </section>

      <button type="button" class="btn btn-primary btn-wide" disabled={checking} onClick={recheck}>
        {checking ? 'Проверяем…' : 'Проверить снова'}
      </button>
      <p class="hint">
        {checked
          ? 'Пока ничего не изменилось: доступа по-прежнему нет. Это не ошибка приложения.'
          : 'Кнопка переспрашивает сервер, появился ли доступ. Ничего не меняет.'}
      </p>
    </div>
  )
}
