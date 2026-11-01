import { Mark } from '@/ui/Brand'
import { AlarmClock, ArrowRight, BellRing, Bot, Pencil, ShieldAlert, XCircle } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { api, type Wait, type WaitPolicy } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useLive } from '@/store/live'
import { useSession } from '@/store/session'
import { AnswerForm, Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { useNames } from '@/ui/useNames'
import { fromUnit, groupWaits, toUnit, UNITS, until, urgency, waited, words, type Unit } from '@/features/waiting/board'

const MAX_REMINDERS = 3 // platform.maxReminders

/** Re-renders every interval, for "waited 12m" and "in 3m" to move. */
function useNow(ms = 15_000) {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms])
  return now
}

/** Every task blocked on someone, grouped by who, with the team's reminder rules. */
export function Waiting() {
  const [data, setData] = useState<{ waiting: Wait[]; policy: WaitPolicy }>()
  const [error, setError] = useState('')
  const now = useNow()
  const seq = useLive((s) => s.model.seq)
  const { name } = useNames()

  const load = useCallback(() => {
    api.waiting().then(setData, (e: Error) => setError(e.message))
  }, [])
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      load()
      return
    }
    const t = setTimeout(load, 400)
    return () => clearTimeout(t)
  }, [seq, load])
  // The policy's timers act on the server without activity of their own.
  useEffect(() => {
    const t = setInterval(load, 30_000)
    return () => clearInterval(t)
  }, [load])

  const groups = useMemo(() => groupWaits(data?.waiting ?? []), [data])
  const oldest = groups[0]?.oldest

  return (
    <div className="scrollbar-thin h-full overflow-y-auto">
      <div className="mx-auto flex max-w-4xl flex-col gap-5 px-4 py-8 sm:px-6">
        <div>
          <h1 className="text-xl font-extrabold">Waiting</h1>
          <p className="mt-1 text-sm text-muted">
            {!data
              ? 'Tasks blocked on someone.'
              : data.waiting.length === 0
                ? 'Nothing is blocked on anyone.'
                : `${data.waiting.length} ${data.waiting.length === 1 ? 'task' : 'tasks'} blocked on ${groups.length} ${groups.length === 1 ? 'address' : 'addresses'}; the longest for ${waited(new Date(oldest!).toISOString(), now)}.`}
          </p>
        </div>
        {error && <p className="text-sm font-semibold text-bad">{error}</p>}
        {data && <Policy policy={data.policy} name={name} onSaved={(policy) => setData({ ...data, policy })} />}
        {!data && <div className="card h-40 animate-pulse" />}
        {data && data.waiting.length === 0 && (
          <div className="card flex flex-col items-center gap-2 p-10 text-center">
            <Mark size={44} motion="idle" />
            <p className="mt-1 font-bold">No one is holding anything up</p>
            <p className="max-w-sm text-sm text-muted">When an agent asks a person something, or waits on another agent, it shows here until they answer.</p>
          </div>
        )}
        {groups.map((g) => (
          <section key={g.who} className="card overflow-hidden">
            <header className="flex items-center gap-3 border-b border-line px-5 py-3">
              <Avatar handle={g.who} kind={g.person ? 'human' : 'agent'} name={name(g.who)} size={34} />
              <div className="min-w-0 flex-1">
                <p className="font-bold">{name(g.who)}</p>
                <p className="text-xs text-muted">
                  {g.waits.length} waiting · oldest {waited(new Date(g.oldest).toISOString(), now)}
                  {!g.person && ' · an agent, so no reminders'}
                </p>
              </div>
            </header>
            <ul className="divide-y divide-line">
              {g.waits.map((w) => (
                <Row key={w.task.id} w={w} now={now} name={name} onChange={load} />
              ))}
            </ul>
          </section>
        ))}
      </div>
    </div>
  )
}

const URGENCY = {
  ok: 'bg-surface-2 text-muted',
  late: 'bg-warn-soft text-warn',
  overdue: 'bg-bad-soft text-bad',
}

function Row({ w, now, name, onChange }: { w: Wait; now: number; name: (h: string) => string; onChange: () => void }) {
  const me = useSession((s) => s.user)
  const [busy, setBusy] = useState(false)
  const [answering, setAnswering] = useState(false)
  const [note, setNote] = useState<{ ok: boolean; text: string }>()
  const mine = !!me && w.who === me.handle
  const escalatedToMe = !!me && !mine && w.escalatedTo === me.handle
  const remind = async () => {
    setBusy(true)
    setNote(undefined)
    try {
      await api.remind(w.task.id)
      setNote({ ok: true, text: 'Reminder sent' })
      onChange()
    } catch (e) {
      setNote({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  const next = [
    w.remindAt && { icon: <BellRing size={11} />, text: `Reminder ${until(w.remindAt, now)}` },
    w.escalateAt && { icon: <ShieldAlert size={11} />, text: `Escalates ${until(w.escalateAt, now)}` },
    w.cancelAt && { icon: <XCircle size={11} />, text: `Cancels ${until(w.cancelAt, now)}` },
  ].filter(Boolean) as { icon: React.ReactNode; text: string }[]

  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3">
      <span className={cn('flex w-14 shrink-0 items-center justify-center gap-1 rounded-full px-2 py-1 font-mono text-xs font-bold', URGENCY[urgency(w, now)])} title={`Waiting since ${new Date(w.since).toLocaleString()}`}>
        {waited(w.since, now)}
      </span>
      <div className="min-w-0 flex-1 basis-60">
        <Link to={`/runs/${w.runId}`} className="block truncate text-sm font-semibold hover:text-accent">
          {w.task.summary || 'Untitled task'}
        </Link>
        <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
          <span>{name(w.asker)} asks</span>
          {w.reminders > 0 && <span className="flex items-center gap-1 font-semibold text-warn"><AlarmClock size={11} /> reminded {w.reminders}×</span>}
          {w.escalatedTo && <span className="flex items-center gap-1 font-semibold text-bad"><ShieldAlert size={11} /> escalated to {name(w.escalatedTo)}</span>}
          {next.map((n) => (
            <span key={n.text} className="flex items-center gap-1">
              {n.icon} {n.text}
            </span>
          ))}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {note && <span className={cn('text-xs font-semibold', note.ok ? 'text-good' : 'text-bad')}>{note.text}</span>}
        {escalatedToMe ? (
          <Button onClick={() => setAnswering(!answering)} title={`Answer in ${name(w.who)}'s place; it is recorded as your answer`}>
            {answering ? 'Close' : `Answer for ${name(w.who)}`}
          </Button>
        ) : mine ? (
          <Link to={`/inbox/${w.asker}`} className="inline-flex items-center gap-1 rounded-lg bg-accent px-3 py-1.5 text-xs font-bold text-accent-ink shadow-card">
            Answer <ArrowRight size={12} />
          </Link>
        ) : w.person ? (
          <Button tone="good" disabled={busy} onClick={() => void remind()} icon={<BellRing size={13} />} title={`Send ${name(w.who)} a reminder now, by email or Slack`}>
            Remind now
          </Button>
        ) : (
          <span className="flex items-center gap-1 text-xs text-muted">
            <Bot size={13} /> agent
          </span>
        )}
      </div>
      {answering && (
        <div className="w-full rounded-xl bg-warn-soft p-3">
          <AnswerForm
            taskId={w.task.id}
            mode={w.task.recipient === w.who ? 'person' : 'sender'}
            schema={w.task.input?.schema}
            compact
            onDone={() => {
              setAnswering(false)
              onChange()
            }}
          />
        </div>
      )}
    </li>
  )
}

/** The policy in one line, and a form for admins to change it. */
function Policy({ policy, name, onSaved }: { policy: WaitPolicy; name: (h: string) => string; onSaved: (p: WaitPolicy) => void }) {
  const me = useSession((s) => s.user)
  const [editing, setEditing] = useState(false)
  const rules = [
    policy.remindAfter && `remind after ${words(policy.remindAfter)} (up to ${MAX_REMINDERS} times)`,
    policy.escalateAfter && policy.escalateTo && `escalate to ${name(policy.escalateTo)} after ${words(policy.escalateAfter)}`,
    policy.cancelAfter && `cancel after ${words(policy.cancelAfter)}`,
  ].filter(Boolean) as string[]

  if (editing) return <PolicyForm policy={policy} onDone={(p) => { setEditing(false); if (p) onSaved(p) }} />
  return (
    <div className="card flex items-start gap-3 px-5 py-4">
      <AlarmClock size={18} className="mt-0.5 shrink-0 text-accent" />
      <div className="min-w-0 flex-1 text-sm">
        <p className="font-bold">Team rules for waiting tasks</p>
        <p className="mt-0.5 text-muted">
          {rules.length ? `When a task waits on a person: ${rules.join(', ')}.` : 'None yet: nobody is reminded and nothing times out. People still get the first notification.'}
        </p>
      </div>
      {me?.admin && (
        <button className="flex shrink-0 items-center gap-1 text-xs font-semibold text-accent" onClick={() => setEditing(true)}>
          <Pencil size={12} /> {rules.length ? 'Edit' : 'Set rules'}
        </button>
      )}
    </div>
  )
}

type Field = { on: boolean; n: number; unit: Unit }
const field = (s: number | null, n: number, unit: Unit): Field => (s ? { on: true, ...toUnit(s) } : { on: false, n, unit })

function PolicyForm({ policy, onDone }: { policy: WaitPolicy; onDone: (p?: WaitPolicy) => void }) {
  const addresses = useSession((s) => s.addresses)
  const people = addresses.filter((a) => a.kind === 'human')
  const [remind, setRemind] = useState(() => field(policy.remindAfter, 1, 'h'))
  const [escalate, setEscalate] = useState(() => field(policy.escalateAfter, 4, 'h'))
  const [to, setTo] = useState(policy.escalateTo || people[0]?.handle || '')
  const [cancel, setCancel] = useState(() => field(policy.cancelAfter, 3, 'd'))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const secs = (f: Field) => (f.on ? fromUnit(f.n, f.unit) : null)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const p: WaitPolicy = { remindAfter: secs(remind), escalateAfter: escalate.on ? secs(escalate) : null, escalateTo: escalate.on ? to : '', cancelAfter: secs(cancel) }
      onDone((await api.setWaitPolicy(p)).policy)
    } catch (err) {
      setError((err as Error).message)
      setBusy(false)
    }
  }

  const row = (label: string, f: Field, set: (f: Field) => void, extra?: React.ReactNode) => (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <label className="flex w-40 items-center gap-2 font-semibold">
        <input type="checkbox" className="h-4 w-4 accent-[var(--accent)]" checked={f.on} onChange={(e) => set({ ...f, on: e.target.checked })} />
        {label}
      </label>
      <span className={cn('flex min-w-0 flex-1 flex-wrap items-center gap-2', !f.on && 'opacity-40')}>
        after
        <input type="number" min={1} className="input w-20" value={f.n} disabled={!f.on} onChange={(e) => set({ ...f, n: Math.max(1, Number(e.target.value) || 1) })} />
        <select className="input w-auto" value={f.unit} disabled={!f.on} onChange={(e) => set({ ...f, unit: e.target.value as Unit })}>
          {UNITS.map((u) => (
            <option key={u.id} value={u.id}>
              {u.label}
            </option>
          ))}
        </select>
        {extra}
      </span>
    </div>
  )

  return (
    <form onSubmit={(e) => void save(e)} className="card flex flex-col gap-3 px-5 py-4">
      <p className="font-bold">Team rules for waiting tasks</p>
      <p className="text-xs text-muted">Timed from when the task started waiting. Reminders and escalations go out by email or Slack, with a one-click answer link.</p>
      {row('Remind them', remind, setRemind, <span className="text-xs text-muted">and again each time, up to {MAX_REMINDERS} times</span>)}
      {row('Escalate', escalate, setEscalate, (
        <>
          to
          <select className="input w-auto" value={to} disabled={!escalate.on} onChange={(e) => setTo(e.target.value)}>
            {people.map((p) => (
              <option key={p.handle} value={p.handle}>
                {p.displayName || p.handle}
              </option>
            ))}
          </select>
          <span className="text-xs text-muted">who can answer in their place</span>
        </>
      ))}
      {row('Cancel the task', cancel, setCancel)}
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || (escalate.on && !to)}>
          Save rules
        </Button>
        <button type="button" className="text-xs font-semibold text-muted hover:text-ink" onClick={() => onDone()}>
          Cancel
        </button>
      </div>
    </form>
  )
}
