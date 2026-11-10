import { Mark, Orbiting } from '@/ui/Brand'
import { ArchiveRestore, ArrowRight, Check, ChevronLeft, ChevronRight, Copy as CopyIcon, Download, Eye, EyeOff, Network, Pause, Play, Search, Webhook } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, type Exchange, type Run, type RunDetail, type RunStatus } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useLive } from '@/store/live'
import { normState } from '@/store/model'
import { useSession } from '@/store/session'
import { Avatar } from '@/ui/Avatar'
import { DataChips } from '@/ui/DataView'
import { StateBadge } from '@/ui/StateBadge'
import { ago, duration } from '@/ui/time'
import { useNames } from '@/ui/useNames'
import { diagnose } from '@/features/runs/diagnose'
import { FailureBanner } from '@/features/runs/FailureBanner'
import { ReportCard } from '@/features/runs/ReportCard'
import { TimeBar } from '@/features/runs/TimeBar'
import { timeSplit } from '@/features/runs/timing'
import { runFileName, runMarkdown } from '@/features/runs/export'
import { KIND_COLOR, Sequence } from '@/features/runs/Sequence'
import { exchangeLine, exchangeParties, pretty, story, type Content, type Step } from '@/features/runs/story'

type Filter = RunStatus | '' | 'hidden'

const FILTERS: { id: Filter; label: string }[] = [
  { id: '', label: 'All' },
  { id: 'running', label: 'Running' },
  { id: 'waiting', label: 'Waiting' },
  { id: 'done', label: 'Done' },
  { id: 'failed', label: 'Failed' },
  { id: 'hidden', label: 'Hidden' },
]

/** Refetch shortly after new activity arrives. */
function useOnActivity(fn: () => void, ms = 400) {
  const seq = useLive((s) => s.model.seq)
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    const t = setTimeout(fn, ms)
    return () => clearTimeout(t)
  }, [seq, fn, ms])
}

export function Runs() {
  const { id } = useParams()
  const navigate = useNavigate()
  const [status, setStatus] = useState<Filter>('')
  const [q, setQ] = useState('')
  const [participant, setParticipant] = useState('')
  const [waitingOn, setWaitingOn] = useState('')
  const [runs, setRuns] = useState<Run[]>()
  const { addresses, user } = useSession()
  const { kind, name } = useNames()

  const load = useCallback(() => {
    void api
      .runs({
        status: status && status !== 'hidden' ? status : undefined,
        archived: status === 'hidden' ? '1' : undefined,
        q: q.trim() || undefined,
        participant: participant || undefined,
        waitingOn: waitingOn || undefined,
      })
      .then((r) => setRuns(r.runs))
      .catch(() => setRuns([]))
  }, [status, q, participant, waitingOn])
  useEffect(() => {
    const t = setTimeout(load, q ? 250 : 0)
    return () => clearTimeout(t)
  }, [load, q])
  useOnActivity(load)

  // Open the newest run when none is picked.
  useEffect(() => {
    if (!id && runs?.length) navigate(`/runs/${runs[0].id}`, { replace: true })
  }, [id, runs, navigate])

  return (
    <div className="flex h-full min-h-0">
      <aside className="flex w-[300px] shrink-0 flex-col border-r border-line bg-surface">
        <div className="space-y-2 border-b border-line p-3">
          <div>
            <h1 className="text-base font-extrabold">Runs</h1>
            <p className="text-[11px] text-muted">Each run is one request and everything it set off.</p>
          </div>
          <label className="flex items-center gap-2 rounded-lg bg-surface-2 px-2.5 py-1.5 text-xs">
            <Search size={13} className="text-muted" />
            <input className="min-w-0 flex-1 bg-transparent outline-none" placeholder="Search: hoodies, @northwind…" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
          <div className="flex flex-wrap gap-1">
            {FILTERS.map((f) => (
              <button
                key={f.id}
                onClick={() => setStatus(f.id)}
                className={cn('rounded-full px-2.5 py-0.5 text-[11px] font-bold', status === f.id ? 'bg-accent text-accent-ink' : 'bg-surface-2 text-muted hover:text-ink')}
              >
                {f.label}
              </button>
            ))}
          </div>
          <div className="flex gap-1.5">
            <select className="input min-w-0 flex-1 py-1 text-xs" value={participant} onChange={(e) => setParticipant(e.target.value)} aria-label="Participant">
              <option value="">Anyone involved</option>
              {addresses.map((a) => (
                <option key={a.handle} value={a.handle}>
                  {name(a.handle)}
                </option>
              ))}
            </select>
            <select className={cn('input min-w-0 flex-1 py-1 text-xs', waitingOn && 'text-warn')} value={waitingOn} onChange={(e) => setWaitingOn(e.target.value)} aria-label="Waiting on">
              <option value="">Waiting on anyone</option>
              {addresses.map((a) => (
                <option key={a.handle} value={a.handle}>
                  Waiting on {name(a.handle)}
                </option>
              ))}
            </select>
          </div>
        </div>
        <ul className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
          {runs?.length === 0 && <li className="p-4 text-xs text-muted">{status === 'hidden' ? 'No hidden runs.' : 'No runs match. Send something from the Inbox.'}</li>}
          {runs?.map((r) => (
            <li key={r.id}>
              <Link to={`/runs/${r.id}`} className={cn('block border-b border-line/60 px-3 py-2.5 hover:bg-surface-2', r.id === id && 'bg-accent-soft hover:bg-accent-soft')}>
                <div className="flex items-start gap-2">
                  <p className="line-clamp-2 min-w-0 flex-1 text-[12.5px] leading-snug font-semibold">{r.title || '(no text)'}</p>
                  {r.state === 'message' ? <span className="rounded-full bg-surface-2 px-2 py-0.5 text-[10px] font-bold text-muted">Message</span> : <StateBadge state={r.state} />}
                </div>
                <div className="mt-1 flex items-center gap-1 text-[11px] text-muted">
                  <span className="truncate">{name(r.starter)}</span>
                  <ArrowRight size={10} className="shrink-0" />
                  <span className="truncate">{name(r.recipient)}</span>
                  <span className="ml-auto shrink-0 font-mono text-[10px]">{ago(r.updatedAt)}</span>
                </div>
                <div className="mt-1 flex items-center gap-1">
                  <div className="flex -space-x-1.5">
                    {r.participants.slice(0, 6).map((h) => (
                      <Avatar key={h} handle={h} kind={kind(h)} name={name(h)} size={18} className="ring-2 ring-surface" />
                    ))}
                  </div>
                  <span className="text-[10.5px] text-muted">
                    {r.tasks > 1 ? `${r.tasks} tasks · ` : ''}
                    {r.entries} events
                    {r.cost ? ` · ${money(r.cost, r.currency)}` : ''}
                    {r.hidden ? <span title="Tasks inside other workspaces, which this one doesn't see"> · +{r.hidden} elsewhere</span> : null}
                  </span>
                  {user && r.waiting.includes(user.handle) && <span className="ml-auto rounded-full bg-warn-soft px-1.5 py-0.5 text-[10px] font-bold text-warn">needs you</span>}
                </div>
                {r.waiting.length > 0 && r.waitingSince && (
                  <p className="mt-1 text-[10.5px] font-semibold text-warn">
                    Waiting on {r.waiting.map(name).join(', ')} for {duration(Date.now() - Date.parse(r.waitingSince))}
                  </p>
                )}
              </Link>
            </li>
          ))}
        </ul>
      </aside>
      <main className="flex min-w-0 flex-1 flex-col">{id ? <RunView key={id} id={id} kind={kind} name={name} onArchived={load} /> : <Empty />}</main>
    </div>
  )
}

function Empty() {
  return (
    <div className="flex flex-1 items-center justify-center p-8 text-center text-sm text-muted">
      <div>
        <div className="mb-3 flex justify-center">
          <Mark size={44} motion="idle" />
        </div>
        Nothing has run yet. Message an agent from the Inbox and its run shows up here, step by step.
        <div className="mt-3 flex justify-center gap-4 font-semibold">
          <Link to="/inbox" className="text-accent">
            Open the Inbox
          </Link>
          <Link to="/agents?new=1" className="text-accent">
            Connect an agent
          </Link>
        </div>
      </div>
    </div>
  )
}

type Speed = 1 | 4 | 'real'

function RunView({ id, kind, name, onArchived }: { id: string; kind: (h: string) => 'agent' | 'human' | undefined; name: (h: string) => string; onArchived: () => void }) {
  const [data, setData] = useState<RunDetail>()
  const [error, setError] = useState('')
  const [params, setParams] = useSearchParams()
  const [tab, setTab] = useState<'story' | 'traffic'>('story')
  const [replaying, setReplaying] = useState(false)
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState<Speed>(1)
  const navigate = useNavigate()
  const openReplay = useLive((s) => s.openReplay)

  const load = useCallback(() => {
    api
      .run(id)
      .then((d) => (setData(d), setError('')))
      .catch((e: Error) => setError(e.message))
  }, [id])
  useEffect(load, [load])
  useOnActivity(load, 600)

  const s = useMemo(() => (data ? story(data, name) : undefined), [data, name])
  const diag = useMemo(() => (data ? diagnose(data, name) : undefined), [data, name])
  const split = useMemo(() => (data ? timeSplit(data.entries, Date.now()) : undefined), [data])
  const archive = async (archived: boolean) => {
    await api.archiveRun(id, archived)
    onArchived()
    if (archived) navigate('/runs')
    else load()
  }
  const steps = useMemo(() => s?.steps ?? [], [s])

  // The step in focus lives in the URL (?step=N), so a step can be linked to.
  const stepParam = Number(params.get('step'))
  const taskParam = params.get('task')
  let focus: number | undefined = stepParam >= 1 && stepParam <= steps.length ? stepParam - 1 : undefined
  if (focus === undefined && taskParam) {
    const i = steps.findIndex((st) => st.taskId === taskParam)
    if (i >= 0) focus = i
  }
  const setFocus = useCallback(
    (i: number | undefined) =>
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('task')
          if (i === undefined) next.delete('step')
          else next.set('step', String(i + 1))
          return next
        },
        { replace: true },
      ),
    [setParams],
  )

  const go = useCallback(
    (delta: number) => {
      setReplaying(true)
      setFocus(Math.max(0, Math.min(steps.length - 1, (focus ?? -1) + delta)))
    },
    [focus, steps.length, setFocus],
  )

  // Play: one step at a time, at a steady pace or with the run's real gaps (capped).
  useEffect(() => {
    if (!playing) return
    if (focus !== undefined && focus >= steps.length - 1) {
      setPlaying(false)
      return
    }
    const cur = focus === undefined ? undefined : steps[focus]
    const next = steps[(focus ?? -1) + 1]
    const wait = speed === 'real' && cur && next ? Math.min(3000, Math.max(200, next.at - cur.at)) : speed === 4 ? 300 : 1100
    const t = setTimeout(() => setFocus((focus ?? -1) + 1), focus === undefined ? 0 : wait)
    return () => clearTimeout(t)
  }, [playing, focus, steps, speed, setFocus])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement)?.closest('input, textarea, select')) return
      if (e.key === 'ArrowRight' || e.key === 'j') go(1)
      else if (e.key === 'ArrowLeft' || e.key === 'k') go(-1)
      else if (e.key === ' ') {
        e.preventDefault()
        setReplaying(true)
        setPlaying((p) => !p)
      } else if (e.key === 'Escape') {
        setReplaying(false)
        setPlaying(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [go])

  if (error) return <p className="p-8 text-sm text-bad">{error}</p>
  if (!data || !s) return <Orbiting label="Loading the run…" className="p-16" />
  const run = data.run
  const start = steps[0]?.at ?? Date.parse(run.startedAt)
  const end = Math.max(...steps.map((x) => x.at), Date.parse(run.updatedAt))
  const exchangeStep = new Map<number, number>()
  steps.forEach((st) => st.exchanges.forEach((x) => exchangeStep.set(x.id, st.n)))

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="border-b border-line bg-surface px-5 py-3">
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h2 className="truncate text-lg font-extrabold" title={run.title}>
                {run.title || '(no text)'}
              </h2>
              {run.state === 'message' ? <span className="rounded-full bg-surface-2 px-2 py-0.5 text-[11px] font-bold text-muted">Message</span> : <StateBadge state={run.state} />}
            </div>
            <p className="mt-0.5 text-xs text-muted">
              {name(run.starter)} → {name(run.recipient)} · {new Date(start).toLocaleString()} · took {duration(split?.segments.length ? split.end - split.start : end - start)} · {steps.length} steps · {data.exchanges.length} HTTP exchanges
              {run.cost ? ` · ${money(run.cost, run.currency)} spent by its agents` : ''}
              {run.waiting.length > 0 && <b className="text-warn"> · waiting for {run.waiting.map(name).join(', ')}</b>}
            </p>
            {!!run.hidden && (
              <p className="mt-0.5 text-[11px] text-muted">
                {run.hidden} {run.hidden === 1 ? 'task' : 'tasks'} of this run happened inside other workspaces: you see the calls between you, not their own work.
              </p>
            )}
            {split && <TimeBar split={split} name={name} />}
          </div>
          <button
            className="flex items-center gap-1.5 rounded-lg border border-line px-2.5 py-1.5 text-xs font-bold text-muted hover:text-ink"
            title="Replay only this run on the live graph"
            onClick={() => {
              openReplay(data.entries, run.title, run.id)
              navigate('/')
            }}
          >
            <Network size={13} /> Watch on graph
          </button>
          <ExportRun markdown={() => runMarkdown({ run, steps, split, exchanges: data.exchanges.length, name })} file={runFileName(run)} />
          <button
            className="flex items-center gap-1.5 rounded-lg border border-line px-2.5 py-1.5 text-xs font-bold text-muted hover:text-ink"
            title={run.archived ? 'Show this run in the list again' : 'Hide this run from the list (a test, say); the Hidden filter still finds it'}
            onClick={() => void archive(!run.archived)}
          >
            {run.archived ? <ArchiveRestore size={13} /> : <EyeOff size={13} />} {run.archived ? 'Unhide' : 'Hide'}
          </button>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <div className="flex rounded-lg bg-surface-2 p-0.5 text-xs font-bold">
            {(['story', 'traffic'] as const).map((t) => (
              <button key={t} onClick={() => setTab(t)} className={cn('rounded-md px-2.5 py-1', tab === t ? 'bg-surface text-ink shadow-card' : 'text-muted')}>
                {t === 'story' ? 'Step by step' : `All traffic (${data.exchanges.length})`}
              </button>
            ))}
          </div>
          {tab === 'story' && (
            <div className="ml-auto flex items-center gap-1.5">
              <button className="rounded-lg p-1.5 text-muted hover:bg-surface-2 hover:text-ink disabled:opacity-40" disabled={focus === 0} onClick={() => go(-1)} title="Previous step (←)">
                <ChevronLeft size={16} />
              </button>
              <button
                className="flex h-8 items-center gap-1.5 rounded-full bg-accent px-3 text-xs font-bold text-accent-ink shadow-card"
                onClick={() => {
                  setReplaying(true)
                  if (!playing && (focus === undefined || focus >= steps.length - 1)) setFocus(undefined)
                  setPlaying(!playing)
                }}
                title="Replay this run (space)"
              >
                {playing ? <Pause size={13} /> : <Play size={13} />} {playing ? 'Pause' : 'Replay'}
              </button>
              <button className="rounded-lg p-1.5 text-muted hover:bg-surface-2 hover:text-ink disabled:opacity-40" disabled={focus === steps.length - 1} onClick={() => go(1)} title="Next step (→)">
                <ChevronRight size={16} />
              </button>
              <div className="flex rounded-lg bg-surface-2 p-0.5">
                {([1, 4, 'real'] as Speed[]).map((sp) => (
                  <button key={sp} onClick={() => setSpeed(sp)} className={cn('rounded-md px-2 py-0.5 font-mono text-[11px] font-bold', sp === speed ? 'bg-surface text-accent shadow-card' : 'text-muted')}>
                    {sp === 'real' ? 'real time' : `${sp}×`}
                  </button>
                ))}
              </div>
              <span className="w-24 text-right font-mono text-[11px] text-muted">{focus === undefined ? `${steps.length} steps` : `step ${focus + 1} / ${steps.length}`}</span>
              {replaying && (
                <button
                  className="flex items-center gap-1 rounded-lg px-2 py-1 text-xs font-bold text-muted hover:bg-surface-2 hover:text-ink"
                  onClick={() => {
                    setReplaying(false)
                    setPlaying(false)
                  }}
                  title="Show every step (Esc)"
                >
                  <Eye size={13} /> Show all
                </button>
              )}
            </div>
          )}
        </div>
      </header>
      {run.report && <ReportCard report={run.report} />}
      {diag && <FailureBanner key={run.id} d={data} diag={diag} name={name} onTraffic={() => setTab('traffic')} />}

      {tab === 'story' ? (
        <div className="flex min-h-0 flex-1">
          <ol className="scrollbar-thin min-h-0 flex-1 space-y-2 overflow-y-auto bg-bg p-4">
            {steps.map((st, i) => (
              <StepCard
                key={st.n}
                step={st}
                start={start}
                active={focus === i}
                hidden={replaying && focus !== undefined && i > focus}
                kind={kind}
                name={name}
                onPick={() => setFocus(focus === i ? undefined : i)}
              />
            ))}
            {steps.length === 0 && <li className="text-sm text-muted">Nothing recorded for this run yet.</li>}
          </ol>
          <section className="hidden w-[min(46%,640px)] shrink-0 flex-col border-l border-line bg-surface md:flex">
            <Sequence steps={steps} lanes={s.lanes} focus={focus} replaying={replaying} kind={kind} name={name} onPick={(i) => setFocus(i)} />
          </section>
        </div>
      ) : (
        <Traffic exchanges={data.exchanges} stepOf={(x) => exchangeStep.get(x.id)} onStep={(n) => (setTab('story'), setFocus(n - 1))} />
      )}
    </div>
  )
}

function rel(at: number, start: number) {
  const d = at - start
  return d < 1000 ? `+${Math.max(0, Math.round(d))}ms` : `+${(d / 1000).toFixed(d < 10_000 ? 1 : 0)}s`
}

function StepCard({
  step: s,
  start,
  active,
  hidden,
  kind,
  name,
  onPick,
}: {
  step: Step
  start: number
  active: boolean
  hidden: boolean
  kind: (h: string) => 'agent' | 'human' | undefined
  name: (h: string) => string
  onPick: () => void
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLLIElement>(null)
  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [active])
  const pushTargets = [...new Set(s.pushes.map((p) => p.to))]
  return (
    <li
      ref={ref}
      className={cn(
        'card cursor-pointer border-l-4 p-3 transition-all duration-300',
        active ? 'ring-2 ring-accent' : 'hover:ring-1 hover:ring-line',
        hidden && 'pointer-events-none opacity-15 blur-[1px]',
      )}
      style={{ borderLeftColor: KIND_COLOR[s.kind] }}
      onClick={onPick}
    >
      <div className="flex items-start gap-2.5">
        <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full font-mono text-[11px] font-bold text-white" style={{ background: KIND_COLOR[s.kind] }}>
          {s.n}
        </span>
        <Avatar handle={s.from} kind={kind(s.from)} name={name(s.from)} size={24} />
        {s.to && (
          <>
            <ArrowRight size={13} className="mt-1.5 shrink-0 text-muted" />
            <Avatar handle={s.to} kind={kind(s.to)} name={name(s.to)} size={24} />
          </>
        )}
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <p className="text-[13px] font-bold">{s.title}</p>
            {s.state && normState(s.state) !== 'unknown' && s.kind !== 'progress' && s.kind !== 'ask' && <StateBadge state={s.state} />}
            <span className="ml-auto shrink-0 font-mono text-[10.5px] text-muted" title={new Date(s.at).toLocaleTimeString()}>
              {new Date(s.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })} · {rel(s.at, start)}
            </span>
          </div>
          <Body c={s.body} />
          {s.reply && (
            <div className="mt-1.5 rounded-lg bg-surface-2 p-2">
              <p className="text-[11px] font-bold text-muted">↳ {name(s.to ?? '')} replied</p>
              <Body c={s.reply} />
            </div>
          )}
          {s.updates.length > 0 && (
            <ul className="mt-1.5 space-y-0.5 border-l-2 border-line pl-2 text-[12px]">
              {s.updates.map((u, i) => (
                <li key={i} className="text-muted">
                  then: <span className="text-ink">{u.text ?? u.fields?.map(([k, v]) => `${k} ${v}`).join(', ')}</span>
                </li>
              ))}
            </ul>
          )}
          {pushTargets.length > 0 && (
            <p className="mt-1.5 flex items-center gap-1 text-[11.5px] text-auth">
              <Webhook size={12} /> Lagrange pushed {s.pushes.length === 1 ? 'this update' : `${s.pushes.length} updates`} to {pushTargets.map((t) => `${name(t)}'s webhook`).join(', ')}
            </p>
          )}
          <div className="mt-1.5 flex items-center gap-3 text-[11px]">
            <button
              className="font-semibold text-muted hover:text-accent"
              onClick={(e) => {
                e.stopPropagation()
                setOpen(!open)
              }}
            >
              {open ? 'Hide' : 'Show'} the raw data ({s.entries.length} event{s.entries.length === 1 ? '' : 's'}, {s.exchanges.length} HTTP)
            </button>
          </div>
          {open && <Raw step={s} />}
        </div>
      </div>
    </li>
  )
}

function Body({ c }: { c?: Content }) {
  if (!c) return null
  return (
    <div className="mt-1 space-y-1">
      {c.text && <p className="text-[12.5px] whitespace-pre-wrap text-ink/90">{c.text}</p>}
      {c.fields && <DataChips fields={c.fields} />}
    </div>
  )
}

function Raw({ step }: { step: Step }) {
  const [tab, setTab] = useState<'events' | 'http'>(step.exchanges.length ? 'http' : 'events')
  return (
    <div className="mt-2 rounded-lg border border-line bg-surface-2 p-2" onClick={(e) => e.stopPropagation()}>
      <div className="mb-2 flex gap-1 text-[11px] font-bold">
        <button className={cn('rounded-md px-2 py-0.5', tab === 'http' ? 'bg-surface text-ink shadow-card' : 'text-muted')} onClick={() => setTab('http')}>
          HTTP ({step.exchanges.length})
        </button>
        <button className={cn('rounded-md px-2 py-0.5', tab === 'events' ? 'bg-surface text-ink shadow-card' : 'text-muted')} onClick={() => setTab('events')}>
          Activity events ({step.entries.length})
        </button>
      </div>
      {tab === 'events' ? (
        <pre className="scrollbar-thin max-h-80 overflow-auto rounded-md bg-surface p-2 font-mono text-[10.5px]">{JSON.stringify(step.entries, null, 2)}</pre>
      ) : step.exchanges.length === 0 ? (
        <p className="text-[11px] text-muted">No HTTP for this step: it happened inside the platform or was recorded with a neighbouring step.</p>
      ) : (
        <div className="space-y-2">
          {step.exchanges.map((x) => (
            <ExchangeView key={x.id} x={x} />
          ))}
        </div>
      )}
    </div>
  )
}

const DIR_TONE: Record<Exchange['direction'], string> = { in: 'bg-work-soft text-work', out: 'bg-good-soft text-good', push: 'bg-auth-soft text-auth' }
const DIR_LABEL: Record<Exchange['direction'], string> = { in: 'call in', out: 'to agent', push: 'webhook' }

function ExchangeView({ x, defaultOpen = false }: { x: Exchange; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  const ok = !x.error && x.status > 0 && x.status < 400
  return (
    <div className="rounded-md border border-line bg-surface">
      <button className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-[11px]" onClick={() => setOpen(!open)}>
        <span className={cn('rounded px-1.5 py-0.5 text-[10px] font-bold', DIR_TONE[x.direction])}>{DIR_LABEL[x.direction]}</span>
        <span className="min-w-0 flex-1 truncate font-mono">{exchangeLine(x)}</span>
        <span className={cn('font-mono font-bold', ok ? 'text-good' : 'text-bad')}>{x.error ? 'ERR' : x.status}</span>
      </button>
      {open && (
        <div className="space-y-1.5 border-t border-line p-2 text-[10.5px]">
          <p className="text-muted">
            {exchangeParties(x)} · {new Date(x.at).toLocaleTimeString()} · {x.durationMs}ms{x.truncated && ' · bodies cut at 64 KB'}
          </p>
          {x.error && <p className="font-semibold text-bad">{x.error}</p>}
          <Body2 title="Request" headers={x.headers?.request} body={x.request} />
          <Body2 title="Response" headers={x.headers?.response} body={x.response} />
        </div>
      )}
    </div>
  )
}

function Body2({ title, headers, body }: { title: string; headers?: Record<string, string>; body: string }) {
  return (
    <div>
      <p className="font-bold text-muted">{title}</p>
      {headers && Object.keys(headers).length > 0 && (
        <p className="font-mono text-muted">
          {Object.entries(headers)
            .map(([k, v]) => `${k}: ${v}`)
            .join(' · ')}
        </p>
      )}
      <pre className="scrollbar-thin mt-0.5 max-h-72 overflow-auto rounded bg-surface-2 p-1.5 font-mono whitespace-pre-wrap">{pretty(body) || '(empty)'}</pre>
    </div>
  )
}

function Traffic({ exchanges, stepOf, onStep }: { exchanges: Exchange[]; stepOf: (x: Exchange) => number | undefined; onStep: (n: number) => void }) {
  const [dir, setDir] = useState<Exchange['direction'] | ''>('')
  const list = exchanges.filter((x) => !dir || x.direction === dir)
  return (
    <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto bg-bg p-4">
      <div className="mb-3 flex flex-wrap items-center gap-1.5 text-[11px]">
        <span className="text-muted">Every HTTP request the platform carried for this run, in order.</span>
        <div className="ml-auto flex gap-1">
          {(['', 'in', 'out', 'push'] as const).map((d) => (
            <button key={d} onClick={() => setDir(d)} className={cn('rounded-full px-2.5 py-0.5 font-bold', dir === d ? 'bg-accent text-accent-ink' : 'bg-surface-2 text-muted')}>
              {d === '' ? 'All' : DIR_LABEL[d]}
            </button>
          ))}
        </div>
      </div>
      <div className="mb-3 grid gap-1 rounded-lg bg-surface p-2 text-[11px] text-muted sm:grid-cols-3">
        <span>
          <b className="text-accent">call in</b>: someone called an address at /a/&lt;handle&gt;
        </span>
        <span>
          <b className="text-good">to agent</b>: Lagrange forwarded it to the agent, through its tunnel
        </span>
        <span>
          <b className="text-auth">webhook</b>: Lagrange pushed a task update to an agent's webhook
        </span>
      </div>
      <ol className="space-y-1.5">
        {list.map((x) => {
          const n = stepOf(x)
          return (
            <li key={x.id} className="flex gap-2">
              <button className="mt-1.5 w-12 shrink-0 text-right font-mono text-[10.5px] text-muted hover:text-accent disabled:hover:text-muted" disabled={!n} onClick={() => n && onStep(n)} title={n ? `Go to step ${n}` : undefined}>
                {n ? `step ${n}` : '—'}
              </button>
              <div className="min-w-0 flex-1">
                <ExchangeView x={x} />
              </div>
            </li>
          )
        })}
        {list.length === 0 && <li className="text-sm text-muted">No HTTP recorded{dir ? ' of this kind' : ''}.</li>}
      </ol>
    </div>
  )
}

/** Copy the run as Markdown, or save it as a .md file: steps, times, parties, the report. */
function ExportRun({ markdown, file }: { markdown: () => string; file: string }) {
  const [copied, setCopied] = useState(false)
  const btn = 'flex items-center gap-1.5 rounded-lg border border-line px-2.5 py-1.5 text-xs font-bold text-muted hover:text-ink'
  return (
    <>
      <button
        className={btn}
        title="Copy the whole run as Markdown: every step with its time, who to whom, kind and title, plus the report"
        onClick={() => {
          void navigator.clipboard?.writeText(markdown())
          setCopied(true)
          setTimeout(() => setCopied(false), 1200)
        }}
      >
        {copied ? <Check size={13} className="text-good" /> : <CopyIcon size={13} />} {copied ? 'Copied' : 'Copy run'}
      </button>
      <button
        className={btn}
        title={`Save the run as ${file}`}
        aria-label="Download the run as Markdown"
        onClick={() => {
          const url = URL.createObjectURL(new Blob([markdown()], { type: 'text/markdown' }))
          const a = document.createElement('a')
          a.href = url
          a.download = file
          a.click()
          setTimeout(() => URL.revokeObjectURL(url), 1000)
        }}
      >
        <Download size={13} /> .md
      </button>
    </>
  )
}

/** "$0.42", or "0.42 EUR" for another currency. */
function money(amount: number, currency?: string): string {
  return !currency || currency === 'USD' ? `$${amount.toFixed(2)}` : `${amount.toFixed(2)} ${currency}`
}
