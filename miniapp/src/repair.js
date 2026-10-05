// Свёртка задания починки в три шага, которые человек может прочесть.
//
// С v0.54 (автопочинка) задание несёт пять шагов лесенки: failover, restart,
// reissue, recreate, failback. Средние три -- ступени одной стадии «Поднимаю
// VPN-туннель»: какая ступень сработала, говорит подстрока. Задания старого
// вида (шаги мастера замены, без restart) ещё лежат в памяти бэкенда сразу
// после выкатки -- для них осталась прежняя свёртка ниже.
//
// Прежнее задание несло восемь: свой failover, шесть шагов мастера замены и свой
// failback. Средние шесть -- это один смысл «выпустил новый конфиг и поднял
// VPN-туннель», и показывать их поштучно значит просить человека читать чеклист
// инженера. Инженерная подробность не теряется -- она в detail каждого шага
// и доступна на экране замены.
const REISSUE = ['issue', 'analyze', 'import', 'handshake', 'promote', 'verify', 'retire']

const RAISE = ['restart', 'reissue', 'recreate']

const LABELS = {
  failover: 'Увожу трафик на запасной VPN-туннель',
  raise: 'Поднимаю VPN-туннель',
  reissue: 'Выпускаю новый конфиг и поднимаю VPN-туннель',
  failback: 'Возвращаю всё на место',
}

// foldState сводит несколько машинных шагов в один человеческий. Провал
// главнее готовности: если внутри что-то упало, стадия провалена, даже если
// соседние шаги успели закрыться.
function foldState(steps, names, jobDone) {
  // skipped -- шаг, до которого дело не дошло (прошла ступень раньше): он
  // не провал и не готовность, его просто нет в счёте.
  const all = steps.filter((s) => names.includes(s.name))
  const mine = all.filter((s) => s.status !== 'skipped')
  if (all.length > 0 && mine.length === 0) return 'skipped'
  if (mine.some((s) => s.status === 'failed')) return 'failed'
  if (mine.length > 0 && mine.every((s) => s.status === 'done')) return 'done'
  if (mine.some((s) => s.status === 'active')) return 'active'
  // Законченное задание не обещает продолжения: недошедший шаг -- пропущен,
  // а не «ждёт». Это тот самый случай, когда «ждёт» на мёртвом задании
  // читается как обещание, которого никто не выполнит.
  return jobDone ? 'skipped' : 'pending'
}

// Стадия «Поднимаю VPN-туннель» -- лесенка restart -> reissue -> recreate.
// Решает последняя ступень, до которой дошло дело: провал ранней ступени при
// удаче следующей -- не провал, это и есть лесенка.
function raiseStage(steps, jobDone) {
  const mine = steps.filter((s) => RAISE.includes(s.name))
  const order = (s) => RAISE.indexOf(s.name)
  const ran = mine.filter((s) => s.status !== 'skipped' && s.status !== 'pending').sort((a, b) => order(a) - order(b))
  const detailOf = (list) => {
    for (let i = list.length - 1; i >= 0; i--) if (list[i].detail) return list[i].detail
    return ''
  }
  const active = mine.find((s) => s.status === 'active')
  if (active) return { state: 'active', sub: active.detail ?? '' }
  if (ran.length === 0) return { state: jobDone ? 'skipped' : 'pending', sub: '' }
  const last = ran[ran.length - 1]
  const moreToCome = !jobDone && mine.some((s) => s.status === 'pending' && order(s) > order(last))
  if (moreToCome) return { state: 'active', sub: last.detail ?? '' }
  return { state: last.status === 'failed' ? 'failed' : 'done', sub: last.detail || detailOf(ran) }
}

// checkName -- туннель этого экрана (tunnel_<id>). С v0.46 сервер называет
// туннель починки (check_name в ответе /repair): своя -- scope 'this', чужая
// -- 'other'. Законченная чужая починка к этому туннелю отношения не имеет:
// для него починки не было, и её шаги/ошибка здесь не рисуются.
// Старый сервер туннель не называет: тогда своим считается только задание,
// запущенное с этого экрана (ownJobID), остальное -- починка роутера
// ('router'), а не этого туннеля.
// pollFailed -- опрос хода не удался. Пока ответа нет вовсе, «узнаю…» с
// вечно спрятанной кнопкой запирало бы человека (review v0.46, п. 2): тогда
// честно говорим, что не узнали, и кнопку показываем.
export function repairView(job, { checkName = '', ownJobID = '', pollFailed = false } = {}) {
  const unknown = job == null && pollFailed
  const loading = job == null && !pollFailed
  const empty = job != null && !job.job_id && !job.state
  const running = Boolean(job?.running)
  let scope = 'router'
  if (job != null && !empty) {
    if (job.check_name && checkName) scope = job.check_name === checkName ? 'this' : 'other'
    else if (ownJobID && job.job_id === ownJobID) scope = 'this'
  }
  // Чужая законченная починка -- для этого туннеля «не было».
  const foreignDone = scope === 'other' && !running
  // Пустой ответ -- починки не было; раньше экран писал над ним «Чиню».
  const idle = empty || foreignDone
  const steps = foreignDone ? [] : (job?.steps ?? [])
  const done = !foreignDone && (job?.state === 'success' || job?.state === 'failed')
  // Шагов ещё нет (задание грузится) -- подписи лесенки: старые задания
  // мастера замены после выкатки v0.54 уже не заводятся, и мелькнувшее
  // «Выпускаю новый конфиг…» было бы неправдой.
  const ladder = steps.length === 0 || steps.some((s) => s.name === 'restart')
  // Лесенка: провал ступени -- ещё не итог; причину называем, когда задание
  // кончилось неудачей (последний провал -- самой верхней ступени).
  const failed = ladder
    ? done && job.state === 'failed'
      ? [...steps].reverse().find((s) => s.status === 'failed')
      : undefined
    : steps.find((s) => s.status === 'failed')
  let title
  if (loading) title = 'Узнаю, идёт ли починка…'
  else if (unknown) title = 'Не удалось узнать, идёт ли починка'
  else if (idle) title = 'Починки ещё не было'
  else if (scope === 'other') title = 'Сейчас чинится другой VPN-туннель'
  else if (scope === 'router') {
    if (running) title = 'На роутере идёт починка'
    else if (done) title = job.state === 'success' ? 'Последняя починка на роутере: готово' : 'Последняя починка на роутере: не получилось'
    else title = 'Последняя починка на роутере'
  } else if (running) title = 'Поднимаю связь'
  else if (done) title = job.state === 'success' ? 'Готово' : 'Не получилось'
  else title = 'Чиню'
  // «Что делать» -- подсказка задания: с v0.54 у лесенки это только действие
  // для человека («обновите ключ…»), причина живёт в шагах (note). Подсказку
  // старого мастера замены так не читаем: там она бывает и причиной.
  const action = ladder && done && job.state === 'failed' ? String(job.hint ?? '').trim().replace(/[.\s]+$/, '') : ''
  return {
    title,
    loading,
    idle,
    scope,
    done,
    ok: done && job?.state === 'success',
    note: failed?.detail ?? '',
    action,
    steps: ladder
      ? (() => {
          const raise = raiseStage(steps, done)
          return [
            // Подстрока увода -- что сделано на самом деле: «трафик идёт через
            // запасной…», «запасного нет…» или (упал резерв) «трафик и так
            // идёт через «A»» -- подпись стадии одна на все случаи.
            { key: 'failover', label: LABELS.failover, state: foldState(steps, ['failover'], done), sub: steps.find((s) => s.name === 'failover')?.detail ?? '' },
            { key: 'raise', label: LABELS.raise, state: raise.state, sub: raise.sub },
            { key: 'failback', label: LABELS.failback, state: foldState(steps, ['failback'], done), sub: '' },
          ]
        })()
      : [
          { key: 'failover', label: LABELS.failover, state: foldState(steps, ['failover'], done), sub: '' },
          { key: 'reissue', label: LABELS.reissue, state: foldState(steps, REISSUE, done), sub: '' },
          { key: 'failback', label: LABELS.failback, state: foldState(steps, ['failback'], done), sub: '' },
        ],
  }
}
