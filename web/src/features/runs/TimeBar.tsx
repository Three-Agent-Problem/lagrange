import { cn } from '@/lib/cn'
import type { Split } from '@/features/runs/timing'
import { duration } from '@/ui/time'

const COLOR = { working: 'bg-work', waiting: 'bg-warn', queued: 'bg-muted/50' }

/** Where the run's time went: a timeline bar and the totals. */
export function TimeBar({ split, name }: { split: Split; name: (h: string) => string }) {
  const total = split.end - split.start
  if (total <= 0 || split.segments.length === 0) return null
  const parts = [
    { key: 'working', label: `${duration(split.working)} working`, color: COLOR.working, show: split.working > 0 },
    ...split.waiting.map((w) => ({ key: `w-${w.who}`, label: `${duration(w.ms)} waiting on ${name(w.who)}`, color: COLOR.waiting, show: true })),
    { key: 'queued', label: `${duration(split.queued)} queued (agent offline or busy)`, color: COLOR.queued, show: split.queued > 0 },
  ].filter((p) => p.show)
  return (
    <div className="mt-2 max-w-3xl">
      <div className="flex h-1.5 overflow-hidden rounded-full bg-surface-2" aria-label="Where the time went">
        {split.segments.map((s, i) => (
          <span
            key={i}
            className={cn('h-full', COLOR[s.kind], i > 0 && 'border-l border-surface')}
            style={{ width: `${((s.to - s.from) / total) * 100}%`, minWidth: 2 }}
            title={`${s.kind === 'waiting' ? `waiting on ${name(s.who!)}` : s.kind} · ${duration(s.to - s.from)}`}
          />
        ))}
      </div>
      <p className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted">
        <b className="text-ink">{duration(total)}</b>
        {parts.map((p) => (
          <span key={p.key} className="flex items-center gap-1">
            <span className={cn('h-2 w-2 rounded-full', p.color)} />
            {p.label}
          </span>
        ))}
      </p>
    </div>
  )
}
