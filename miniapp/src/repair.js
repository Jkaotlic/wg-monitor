// Свёртка задания починки в три шага, которые человек может прочесть.
//
// Задание несёт восемь: свой failover, шесть шагов мастера замены и свой
// failback. Средние шесть -- это один смысл «выпустил новый конфиг и поднял
// линию», и показывать их поштучно значит просить человека читать чеклист
// инженера. Инженерная подробность не теряется -- она в detail каждого шага
// и доступна на экране замены.
const REISSUE = ['issue', 'import', 'handshake', 'promote', 'verify', 'retire']

const LABELS = {
  failover: 'Увожу трафик на запасную линию',
  reissue: 'Выпускаю новый конфиг и поднимаю линию',
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

export function repairView(job) {
  const steps = job?.steps ?? []
  const done = job?.state === 'success' || job?.state === 'failed'
  const failed = steps.find((s) => s.status === 'failed')
  return {
    title: done ? (job.state === 'success' ? 'Готово' : 'Не получилось') : 'Чиню',
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
