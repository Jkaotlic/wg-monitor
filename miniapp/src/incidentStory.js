export function incidentStoryView(data) {
  if (Array.isArray(data?.story) && data.story.length > 0) {
    return data.story.map((step) => ({
      at: step.at,
      kind: step.kind,
      tone: step.tone || 'muted',
      title: step.title,
      ...(step.detail ? { detail: step.detail } : {}),
      ...(step.command_id ? { commandID: step.command_id } : {}),
    }))
  }
  return (data?.transitions ?? []).map((transition) => ({
    at: transition.ts,
    kind: `transition_${transition.status}`,
    tone: transition.status === 'fail' ? 'danger' : 'ok',
    title: transition.label || (transition.status === 'fail' ? 'Сбой' : 'В норме'),
  }))
}
