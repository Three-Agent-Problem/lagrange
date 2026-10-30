import { cn } from '@/lib/cn'
import { normState, type State } from '@/store/model'

export const STATE_LABEL: Record<State, string> = {
  submitted: 'Queued',
  working: 'Working',
  input_required: 'Needs input',
  auth_required: 'Needs sign-in',
  completed: 'Done',
  failed: 'Failed',
  canceled: 'Canceled',
  rejected: 'Rejected',
  unknown: 'Unknown',
}

export const STATE_TONE: Record<State, string> = {
  submitted: 'bg-surface-2 text-muted',
  working: 'bg-accent-soft text-accent',
  input_required: 'bg-warn-soft text-warn',
  auth_required: 'bg-auth-soft text-auth',
  completed: 'bg-good-soft text-good',
  failed: 'bg-bad-soft text-bad',
  canceled: 'bg-surface-2 text-muted',
  rejected: 'bg-bad-soft text-bad',
  unknown: 'bg-surface-2 text-muted',
}

/** The CSS color of a state, for strokes and rings. */
export const STATE_COLOR: Record<State, string> = {
  submitted: 'var(--muted)',
  working: 'var(--accent)',
  input_required: 'var(--warn)',
  auth_required: 'var(--auth)',
  completed: 'var(--good)',
  failed: 'var(--bad)',
  canceled: 'var(--muted)',
  rejected: 'var(--bad)',
  unknown: 'var(--muted)',
}

export function StateBadge({ state, className }: { state: string | State; className?: string }) {
  const s = normState(state) === 'unknown' ? (state as State) : normState(state)
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-bold whitespace-nowrap', STATE_TONE[s] ?? STATE_TONE.unknown, className)}>
      {(s === 'working' || s === 'submitted') && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />}
      {STATE_LABEL[s] ?? s}
    </span>
  )
}
