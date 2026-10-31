import { ExternalLink, Rewind } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { api, type RunDetail } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useLive } from '@/store/live'
import { DataChips } from '@/ui/DataView'
import { clock } from '@/ui/time'
import { KIND_COLOR } from '@/features/runs/Sequence'
import { story, type Content } from '@/features/runs/story'

/** A run's steps, compact, right where it was clicked; focus marks the step holding that event. */
export function RunPeek({ runId, name, focus }: { runId: string; name: (h: string) => string; focus?: number }) {
  const [data, setData] = useState<RunDetail>()
  const [error, setError] = useState('')
  const seq = useLive((s) => s.model.seq)
  const openReplay = useLive((s) => s.openReplay)
  const first = useRef(true)

  // Load now, then follow the run while it is open.
  useEffect(() => {
    const load = () => api.run(runId).then(setData, (e: Error) => setError(e.message))
    if (first.current) {
      first.current = false
      void load()
      return
    }
    const t = setTimeout(() => void load(), 500)
    return () => clearTimeout(t)
  }, [runId, seq])

  const steps = useMemo(() => (data ? story(data, name).steps : []), [data, name])

  if (error) return <p className="px-4 py-2 text-xs text-bad">{error}</p>
  if (!data) return <div className="mx-4 my-2 h-16 animate-pulse rounded-lg bg-surface-2" />
  return (
    <div className="px-4 pt-1 pb-3">
      <ol className="relative flex flex-col gap-2 border-l border-line pl-4">
        {steps.map((s) => {
          const focused = focus !== undefined && s.entries.some((e) => e.seq === focus)
          return (
            <li key={s.n} className={cn('relative text-xs', focused && '-mx-2 rounded-lg bg-accent-soft/70 px-2 py-1')}>
              <span className={cn('absolute top-0.5 flex h-4 w-4 items-center justify-center rounded-full font-mono text-[9px] font-bold text-white', focused ? '-left-[18px]' : '-left-[26px]')} style={{ background: KIND_COLOR[s.kind] }}>
                {s.n}
              </span>
              <p className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 font-semibold">{s.title}</span>
                <span className="shrink-0 font-mono text-[10px] text-muted">{clock(s.at)}</span>
              </p>
              <Short c={s.body} />
              {s.reply && (
                <div className="mt-0.5 border-l-2 border-good/40 pl-1.5">
                  <Short c={s.reply} />
                </div>
              )}
            </li>
          )
        })}
      </ol>
      <div className="mt-3 flex items-center gap-3 text-[11px] font-semibold">
        {!runId.startsWith('msg-') && (
          <button className="flex items-center gap-1 text-accent" onClick={() => openReplay(data.entries, data.run.title, data.run.id)} title="Play this run back on the graph">
            <Rewind size={12} /> Replay on the graph
          </button>
        )}
        <Link to={`/runs/${data.run.id}`} className="flex items-center gap-1 text-muted hover:text-ink">
          <ExternalLink size={11} /> Full detail and HTTP
        </Link>
      </div>
    </div>
  )
}

function Short({ c }: { c?: Content }) {
  if (!c) return null
  return (
    <div className="mt-0.5 flex flex-col gap-1 text-muted">
      {c.text && <p className="line-clamp-3 whitespace-pre-wrap">{c.text}</p>}
      {c.fields && <DataChips fields={c.fields.slice(0, 6)} />}
    </div>
  )
}
