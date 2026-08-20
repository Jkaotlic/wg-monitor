import { useState } from 'preact/hooks'
import { fetchRouters } from '../api.js'

// Первый вход человека, которому ещё не выдали доступ. Раньше он видел пустой
// экран, неотличимый от поломки; пустой доступ -- это состояние системы, а не
// ошибка приложения, и говорить о нём надо прямо, вместе с тем, что делать.
//
// Шаги здесь -- РЕАЛЬНЫЕ, а не из макета: кода на шесть цифр в wg-monitor нет,
// привязка Telegram случается либо командой администратора, либо первым
// нажатием в теме своего роутера (TOFU, db.User.TelegramUserID). Нарисовать
// красивый несуществующий сценарий значило бы отправить человека делать то,
// чего система не умеет.
export function NoAccess({ onRetry }) {
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
          <div class="steps">
            <div class="step">
              <b class="step-num">1</b>
              <span class="step-main">
                Попросите администратора выдать доступ
                <u class="step-note">
                  Ему нужно ваше имя в Telegram или числовой id — он добавит вас владельцем или
                  оператором роутера.
                </u>
              </span>
            </div>
            <div class="step">
              <b class="step-num">2</b>
              <span class="step-main">
                Нажмите любую кнопку в теме своего роутера
                <u class="step-note">
                  Бот запоминает того, кто первым нажал в теме роутера, и связывает с ним аккаунт.
                </u>
              </span>
            </div>
            <div class="step">
              <b class="step-num">3</b>
              <span class="step-main">
                Откройте приложение заново
                <u class="step-note">Дальше оно будет открываться сразу на вашем роутере.</u>
              </span>
            </div>
          </div>
          <p class="card-foot">
            <b>Пароль от роутера приложение не спрашивает никогда.</b> Оно разговаривает с ботом, а
            бот — с агентом, который уже стоит на роутере.
          </p>
        </div>
      </section>

      <button type="button" class="btn btn-primary btn-wide" disabled={checking} onClick={recheck}>
        {checking ? 'Спрашиваем бота…' : 'Проверить снова'}
      </button>
      <p class="hint">
        {checked
          ? 'Пока ничего не изменилось: доступа по-прежнему нет. Это не ошибка приложения.'
          : 'Кнопка спрашивает бота, появился ли доступ. Ничего не меняет.'}
      </p>
    </div>
  )
}
