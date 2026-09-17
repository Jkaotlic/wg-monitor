// «Ход работы»: подписи шагов и что сказать о задании. Задание -- то, что
// отдаёт GET /v1/miniapp/jobs/{id} (provision.Job): установка, переустановка
// или перенаправление агента. Экран один на все три; различаются слова.

export const JOB_STATES = ['running', 'success', 'failed']
export const STEP_STATUSES = ['pending', 'active', 'done', 'failed']

// Все 10 имён provision/steps.go. Порядок шагов задаёт сервер (массив steps),
// здесь только слова. Сверка с Go -- test/jobSteps.test.js.
const STEP_LABELS = {
  terminal_connected: 'Вход в терминал роутера',
  arch_detected: 'Определение архитектуры роутера',
  downloading: 'Скачивание агента',
  checksum_ok: 'Проверка подписи и контрольной суммы',
  config_written: 'Запись настроек агента',
  init_installed: 'Установка автозапуска',
  service_started: 'Запуск агента',
  backend_url_rewritten: 'Смена адреса сервера',
  service_restarted: 'Перезапуск агента',
  verify_online: 'Агент выходит на связь',
}

export const STEP_NAMES = Object.keys(STEP_LABELS)

// Незнакомый шаг (сервер новее бандла) показывается своим именем: пустая
// строка в списке шагов хуже непереведённого слова.
export function stepLabel(name) {
  const key = typeof name === 'string' ? name : ''
  return STEP_LABELS[key] ?? key
}

const KIND_WORDS = {
  provision: { running: 'Установка агента идёт', done: 'Агент установлен и на связи' },
  repair_reinstall: { running: 'Переустановка агента идёт', done: 'Агент переустановлен и на связи' },
  repair_repoint: { running: 'Перенаправление агента идёт', done: 'Агент перенаправлен и на связи' },
}

export function jobTitle(kind, nickname) {
  const nick = typeof nickname === 'string' ? nickname : ''
  switch (kind) {
    case 'repair_reinstall':
      return `Переустановка агента на «${nick}»`
    case 'repair_repoint':
      return `Перенаправление агента «${nick}»`
    default:
      return `Установка агента на «${nick}»`
  }
}

export const JOB_TEXTS = {
  loading: 'Спрашиваем сервер…',
  reconnecting: 'Переподключение…',
  expired: 'Задание завершилось больше 30 минут назад',
  expiredSub: 'Сервер хранит итог задания 30 минут. Как дела у роутера сейчас — смотрите в Парке.',
  lost: 'Нет связи с сервером',
  lostSub: 'Задание могло продолжиться на сервере. Откройте этот экран заново, чтобы проверить.',
  leave: 'Уход с экрана задание не отменяет: оно продолжится на сервере.',
  details: 'Подробности',
  openRouter: 'Открыть роутер',
  close: 'Закрыть',
}

export function jobView(job) {
  const words = KIND_WORDS[job?.kind] ?? KIND_WORDS.provision
  const steps = (Array.isArray(job?.steps) ? job.steps : []).map((s) => ({
    name: typeof s?.name === 'string' ? s.name : '',
    label: stepLabel(s?.name),
    status: STEP_STATUSES.includes(s?.status) ? s.status : 'pending',
    detail: typeof s?.detail === 'string' ? s.detail : '',
  }))
  const state = job?.state
  const success = state === 'success'
  const failed = state === 'failed'
  let headline = words.running
  let tone = 'running'
  if (success) {
    headline = job?.version ? `${words.done} · ${job.version}` : words.done
    tone = 'ok'
  } else if (failed) {
    const step = steps.find((s) => s.status === 'failed')
    headline = step ? `Не получилось: ${step.label}` : 'Не получилось'
    tone = 'bad'
  }
  return {
    headline,
    tone,
    steps,
    finished: success || failed,
    success,
    // hint и tail -- только про провал: у идущего задания они пусты или
    // устарели, а успеху подробности не нужны.
    hint: failed && typeof job?.hint === 'string' ? job.hint : '',
    tail: failed && typeof job?.tail === 'string' ? job.tail : '',
    nickname: typeof job?.nickname === 'string' ? job.nickname : '',
    routerID: Number.isInteger(job?.router_id) ? job.router_id : null,
  }
}
