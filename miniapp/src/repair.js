// Свёртка задания починки в три шага, которые человек может прочесть.
//
// Задание несёт восемь: свой failover, шесть шагов мастера замены и свой
// failback. Средние шесть -- это один смысл «выпустил новый конфиг и поднял
// VPN-туннель», и показывать их поштучно значит просить человека читать чеклист
// инженера. Инженерная подробность не теряется -- она в detail каждого шага
// и доступна на экране замены.
const REISSUE = ['issue', 'analyze', 'import', 'handshake', 'promote', 'verify', 'retire']

const LABELS = {
  failover: 'Увожу трафик на запасной VPN-туннель',
  reissue: 'Выпускаю новый конфиг и поднимаю VPN-туннель',
  failback: 'Возвращаю всё на место',
}

// foldState сводит несколько машинных шагов в один человеческий. Провал
// главнее готовности: если внутри что-то упало, стадия провалена, даже если
// соседние шаги успели закрыться.
function foldState(steps, names, jobDone) {
  const mine = steps.filter((s) => names.includes(s.name))
  if (mine.some((s) => s.status === 'failed')) return 'failed'
  if (mine.length > 0 && mine.every((s) => s.status === 'done')) return 'done'
  if (mine.some((s) => s.status === 'active')) return 'active'
  // Законченное задание не обещает продолжения: недошедший шаг -- пропущен,
  // а не «ждёт». Это тот самый случай, когда «ждёт» на мёртвом задании
  // читается как обещание, которого никто не выполнит.
  return jobDone ? 'skipped' : 'pending'
}

// ownJobID -- задание, запущенное с этого экрана. Сервер отдаёт последнюю
// починку РОУТЕРА, без имени туннеля (MINI-05): чужое задание может быть
// починкой другого туннеля, и выдавать его ход за ход этого -- ложь. Такое
// задание названо починкой роутера (scope 'router'), своё -- 'this'.
// pollFailed -- опрос хода не удался. Пока ответа нет вовсе, «узнаю…» с
// вечно спрятанной кнопкой запирало бы человека (review v0.46, п. 2): тогда
// честно говорим, что не узнали, и кнопку показываем.
export function repairView(job, { ownJobID = '', pollFailed = false } = {}) {
  const steps = job?.steps ?? []
  const unknown = job == null && pollFailed
  const loading = job == null && !pollFailed
  // Пустой ответ -- починки не было; раньше экран писал над ним «Чиню».
  const idle = job != null && !job.job_id && !job.state
  const done = job?.state === 'success' || job?.state === 'failed'
  const running = Boolean(job?.running)
  const failed = steps.find((s) => s.status === 'failed')
  const scope = job != null && !idle && ownJobID && job.job_id === ownJobID ? 'this' : 'router'
  let title
  if (loading) title = 'Узнаю, идёт ли починка…'
  else if (unknown) title = 'Не удалось узнать, идёт ли починка'
  else if (idle) title = 'Починки ещё не было'
  else if (scope === 'router') {
    if (running) title = 'На роутере идёт починка'
    else if (done) title = job.state === 'success' ? 'Последняя починка на роутере: готово' : 'Последняя починка на роутере: не получилось'
    else title = 'Последняя починка на роутере'
  } else if (running) title = 'Поднимаю связь'
  else if (done) title = job.state === 'success' ? 'Готово' : 'Не получилось'
  else title = 'Чиню'
  return {
    title,
    loading,
    idle,
    scope,
    done,
    ok: job?.state === 'success',
    note: failed?.detail ?? '',
    steps: [
      { key: 'failover', label: LABELS.failover, state: foldState(steps, ['failover'], done) },
      { key: 'reissue', label: LABELS.reissue, state: foldState(steps, REISSUE, done) },
      { key: 'failback', label: LABELS.failback, state: foldState(steps, ['failback'], done) },
    ],
  }
}
