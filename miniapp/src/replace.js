// Мастер замены конфига: перевод задания движка в то, что видит человек.
//
// Он смотрит на этот список, пока линия меняется под ним, поэтому шаги
// названы последствием («Ждём рукопожатия новой линии»), а не именем шага в
// коде. Провал обязан сказать и причину, и что уже откачено: вопрос человека
// в этот момент -- «в каком состоянии остался роутер», а не «какой шаг упал».

const STEP_TITLES = {
  issue: 'Выпускаем конфиг у провайдера',
  import: 'Кладём новый туннель рядом с прежним',
  handshake: 'Ждём рукопожатия новой линии',
  promote: 'Переводим политику на новый туннель',
  verify: 'Проверяем, каким адресом видно снаружи',
  retire: 'Выключаем прежний туннель',
}

const STEP_TONE = { done: 'ok', active: 'sig', failed: 'danger', pending: 'off' }

export function stepTitle(name) {
  return STEP_TITLES[name] ?? name
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
