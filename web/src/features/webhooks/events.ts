/** What each event means, for the checkboxes. */
export const EVENT_LABELS: Record<string, string> = {
  'task.waiting': 'A task waits on someone (a question or approval)',
  'task.answered': 'Someone answered a waiting task',
  'run.completed': 'A run finished',
  'run.failed': 'A run failed, was rejected or canceled',
  'address.online': 'An agent came online',
  'address.offline': 'An agent went offline',
}

/** A short line for a delivery's outcome. */
export function deliveryLine(d: { status: string; attempts: number; lastStatus?: number; lastError?: string }): string {
  const tries = d.attempts === 1 ? '1 try' : `${d.attempts} tries`
  if (d.status === 'delivered') return `${d.lastStatus ?? 200} · ${tries}`
  const why = d.lastError || (d.lastStatus ? `HTTP ${d.lastStatus}` : '')
  if (d.status === 'failed') return `gave up after ${tries}${why ? ` · ${why}` : ''}`
  return d.attempts === 0 ? 'queued' : `retrying · ${tries}${why ? ` · ${why}` : ''}`
}
