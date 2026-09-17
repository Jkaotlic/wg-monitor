// Мастер замены конфига: перевод задания движка в то, что видит человек.
//
// Он смотрит на этот список, пока VPN-туннель меняется под ним, поэтому
// шаги названы последствием («Ждём, когда новый VPN-туннель обменяется
// ключами»), а не именем шага в
// коде. Провал обязан сказать и причину, и что уже откачено: вопрос человека
// в этот момент -- «в каком состоянии остался роутер», а не «какой шаг упал».

const STEP_TITLES = {
  issue: 'Выпускаем конфиг у провайдера',
  analyze: 'Проверяем, примет ли роутер конфиг',
  import: 'Поднимаем новый VPN-туннель рядом с прежним',
  handshake: 'Ждём, когда новый VPN-туннель обменяется ключами',
  promote: 'Переводим правила на новый VPN-туннель',
  verify: 'Проверяем, каким адресом видно снаружи',
  retire: 'Выключаем прежний VPN-туннель',
}

const STEP_TONE = { done: 'ok', active: 'sig', failed: 'danger', pending: 'off' }

export function stepTitle(name) {
  return STEP_TITLES[name] ?? name
}

// Что написать справа от шага. Зависит не только от самого шага, но и от
// того, живо ли задание: «ждёт» в законченном задании обещает продолжение,
// которого не будет -- мастер остановился на упавшем шаге и не вернётся.
export function stepValue(status, running) {
  switch (status) {
    case 'done':
      return 'готово'
    case 'active':
      return 'идёт'
    case 'failed':
      return 'не вышло'
    default:
      return running ? 'ждёт' : 'не начинали'
  }
}

export function replaceView(job) {
  const steps = job?.steps ?? []
  if (!job || !job.job_id) {
    return { idle: true, running: false, steps: [], headline: '', tone: 'off', rollback: '', current: null }
  }
  const view = steps.map((s) => ({
    name: s.name,
    title: stepTitle(s.name),
    status: s.status,
    detail: s.detail ?? '',
    tone: STEP_TONE[s.status] ?? 'off',
  }))
  const current = view.find((s) => s.status === 'active') ?? null
  const failed = job.state === 'failed'
  // Откат движок дописывает в ту же подсказку после точки: разнести их --
  // значит потерять связь причины и последствия.
  const [reason, rollback] = splitHint(job.hint ?? '')
  return {
    idle: false,
    running: Boolean(job.running) || job.state === 'running',
    steps: view,
    current,
    tone: failed ? 'danger' : job.state === 'success' ? 'ok' : 'sig',
    headline: reason,
    rollback,
  }
}

function splitHint(hint) {
  const at = hint.indexOf('. Откат: ')
  if (at < 0) return [hint, '']
  return [hint.slice(0, at), hint.slice(at + '. Откат: '.length)]
}

// Почему замена не началась -- словами. Сервер присылает код причины, а
// ApiError.message -- это «/routers/5/replace failed: 400»: путь и число,
// то есть внутренности запроса вместо ответа на вопрос «и что теперь».
//
// Незнакомый код не выдумываем: говорим, что не начали, и оставляем номер --
// по нему причину найдут в логе.
const START_ERRORS = {
  agent_too_old:
    'Агент на этом роутере слишком старый: у него нет команд, которыми мастер переключает VPN-туннель и откатывает изменения. Обновите агента и повторите — до обновления замена не начнётся.',
  already_running: 'На этом роутере уже идёт замена конфига — дождитесь, пока она закончится.',
  not_configured: 'Замена конфигов на сервере не настроена: выпускать конфиг не из чего.',
  unknown_tunnel: 'Роутер не знает VPN-туннель, выбранный для замены. Обновите экран VPN-туннелей и выберите заново.',
  unknown_provider: 'Этот кабинет приложению неизвестен.',
  missing_fields: 'Не хватает выбора: нужны кабинет, вариант и VPN-туннель, который меняем.',
}

export function startErrorText(err) {
  const code = err?.code
  if (code && START_ERRORS[code]) return START_ERRORS[code]
  if (!err?.status) return 'Не удалось связаться с сервером — замена не началась.'
  return `Замена не началась: сервер ответил ${err.status}.`
}

// Что мастер оставил на роутере и куда с этим идти. Замена не удаляет ничего:
// при успехе остаётся прежний VPN-туннель (выключенным), при откате -- новый.
// Удаление необратимо, поэтому разбираться с оставленным человек будет на
// экране этого VPN-туннеля, а не здесь.
export function replaceLeftover(view, tunnel) {
  if (!view || view.idle || view.running) return null
  const name = tunnel?.name || tunnel?.id || ''
  if (view.tone === 'ok' && tunnel?.id) {
    return {
      tunnelID: tunnel.id,
      button: `К VPN-туннелю «${name}»`,
      text: `Прежний VPN-туннель «${name}» остался на роутере выключенным. Разбираться с ним — оставить про запас или удалить — будете на его собственном экране.`,
    }
  }
  if (view.tone === 'danger' && /оставлен на роутере/.test(view.rollback ?? '')) {
    return {
      tunnelID: null,
      button: 'К списку VPN-туннелей',
      text: 'Новый VPN-туннель выключен и оставлен на роутере. Разбираться с ним — удалить или попробовать снова — будете на его собственном экране в списке VPN-туннелей.',
    }
  }
  return null
}
