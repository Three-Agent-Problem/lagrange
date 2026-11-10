import { Check, Send, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { api, type Answer, type AskQuestion, type JSONSchema } from '@/lib/api'
import { formatAnswers, toggle } from '@/lib/ask'
import { cn } from '@/lib/cn'
import { formData, formFields } from '@/lib/schemaForm'

type Props = {
  taskId: string
  /** "person": the task is addressed to me; "sender": the agent asked me back. */
  mode: 'person' | 'sender'
  /** A structured input request, when the agent sent a schema. */
  schema?: JSONSchema
  /** Multiple-choice questions (lagrange/ask), answered as one line per question. */
  questions?: AskQuestion[]
  /** The task asks to approve or reject. */
  approval?: boolean
  compact?: boolean
  onDone?: () => void
  /** Sends the answer; the default answers as the logged-in person. */
  send?: (a: Answer) => Promise<void>
}

/** Answers a task that waits for input: approve or reject, a structured form, or text. */
export function AnswerForm({ taskId, mode, schema, questions, approval, compact, onDone, send = (a) => api.answer(taskId, a) }: Props) {
  if (questions?.length && !schema) return <QuestionsForm questions={questions} compact={compact} onDone={onDone} send={send} />
  return <PlainForm taskId={taskId} mode={approval ? 'person' : mode} schema={schema} compact={compact} onDone={onDone} send={send} />
}

/** Each question's options as chips to pick, and a line for your own words. */
function QuestionsForm({ questions, compact, onDone, send }: { questions: AskQuestion[]; compact?: boolean; onDone?: () => void; send: (a: Answer) => Promise<void> }) {
  const [picks, setPicks] = useState<number[][]>([])
  const [other, setOther] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await send({ text: formatAnswers(questions, picks, other) })
      onDone?.()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const answered = questions.every((_, i) => (picks[i]?.length ?? 0) > 0 || (other[i] ?? '').trim())
  return (
    <form onSubmit={submit} className={cn('nodrag nopan flex flex-col gap-3', compact && 'gap-2')} onClick={(e) => e.stopPropagation()}>
      {questions.map((q, qi) => (
        <fieldset key={qi} className="flex flex-col gap-1.5">
          <legend className="text-xs font-semibold">
            {q.header && <span className="mr-1.5 rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] uppercase text-muted">{q.header}</span>}
            {q.question}
            {q.multi_select && <span className="ml-1 font-normal text-muted">(pick any)</span>}
          </legend>
          <div className="flex flex-wrap gap-1.5">
            {q.options.map((o, oi) => {
              const on = picks[qi]?.includes(oi)
              return (
                <button
                  key={oi}
                  type="button"
                  title={o.description}
                  aria-pressed={on}
                  onClick={() => setPicks(toggle(questions, picks, qi, oi))}
                  className={cn('rounded-lg border px-2.5 py-1 text-xs font-semibold transition', on ? 'border-accent bg-accent text-accent-ink' : 'border-line hover:bg-surface-2')}
                >
                  {on && <Check size={12} strokeWidth={3} className="mr-1 inline" />}
                  {o.label}
                </button>
              )
            })}
          </div>
          <input className="input" placeholder="Or in your own words…" value={other[qi] ?? ''} onChange={(e) => setOther(Object.assign([...other], { [qi]: e.target.value }))} />
        </fieldset>
      ))}
      <div>
        <Button tone="accent" type="submit" disabled={busy || !answered} icon={<Send size={13} />}>
          Send answers
        </Button>
      </div>
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}
    </form>
  )
}

function PlainForm({ mode, schema, compact, onDone, send }: Omit<Props, 'questions' | 'approval'> & { send: (a: Answer) => Promise<void> }) {
  const [text, setText] = useState('')
  const [values, setValues] = useState<Record<string, string | boolean>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const fields = schema ? formFields(schema) : []

  const submit = async (answer: Answer) => {
    setBusy(true)
    setError('')
    try {
      await send(answer)
      setText('')
      onDone?.()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const submitForm = (extra: Record<string, boolean> = {}) => {
    if (!schema) return
    const res = formData(schema, { ...values, ...extra })
    if ('error' in res) setError(res.error)
    else void submit({ data: res.data, text: text || undefined })
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (fields.length) submitForm()
    else if (text.trim()) void submit({ text })
  }

  const decision = fields.find((f) => f.kind === 'decision')

  return (
    <form onSubmit={onSubmit} className={cn('nodrag nopan flex flex-col gap-2', compact && 'gap-1.5')} onClick={(e) => e.stopPropagation()}>
      {fields
        .filter((f) => f.kind !== 'decision')
        .map((f) => (
          <label key={f.name} className="flex flex-col gap-1 text-xs font-semibold text-muted">
            <span>
              {f.title}
              {f.required && <span className="text-bad"> *</span>}
            </span>
            {f.kind === 'select' ? (
              <select className="input" value={String(values[f.name] ?? '')} onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}>
                <option value="">Choose…</option>
                {f.options.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            ) : f.kind === 'checkbox' ? (
              <input type="checkbox" className="h-4 w-4 accent-[var(--accent)]" checked={values[f.name] === true} onChange={(e) => setValues({ ...values, [f.name]: e.target.checked })} />
            ) : (
              <input
                className="input"
                inputMode={f.kind === 'number' || f.kind === 'integer' ? 'decimal' : undefined}
                value={String(values[f.name] ?? '')}
                onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
              />
            )}
          </label>
        ))}
      {(mode === 'person' || !fields.length || (decision && fields.length === 1)) && (
        <input
          className="input"
          placeholder={mode === 'person' ? 'Add a note (optional)…' : fields.length ? 'Add a note…' : 'Reply…'}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      )}
      <div className="flex gap-1.5">
        {decision ? (
          <>
            <Button tone="good" disabled={busy} onClick={() => submitForm({ [decision.name]: true })} icon={<Check size={14} strokeWidth={3} />}>
              {decision.title === decision.name ? 'Approve' : decision.title}
            </Button>
            <Button tone="bad" disabled={busy} onClick={() => submitForm({ [decision.name]: false })} icon={<X size={14} strokeWidth={3} />}>
              Decline
            </Button>
          </>
        ) : mode === 'person' ? (
          <>
            <Button tone="good" disabled={busy} onClick={() => void submit({ text: text.trim() || 'Approved.' })} icon={<Check size={14} strokeWidth={3} />}>
              Approve
            </Button>
            <Button tone="bad" disabled={busy} onClick={() => void submit({ text: text.trim() || 'Rejected.', decision: 'reject' })} icon={<X size={14} strokeWidth={3} />}>
              Reject
            </Button>
          </>
        ) : (
          <Button tone="accent" type="submit" disabled={busy || (!fields.length && !text.trim())} icon={<Send size={13} />}>
            {fields.length ? 'Submit' : 'Reply'}
          </Button>
        )}
      </div>
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}
    </form>
  )
}

const TONES = {
  good: 'bg-good text-white',
  bad: 'bg-bad-soft text-bad hover:bg-bad hover:text-white',
  accent: 'bg-accent text-accent-ink',
}

export function Button({
  tone = 'accent',
  icon,
  children,
  className,
  type = 'button',
  ...rest
}: { tone?: keyof typeof TONES; icon?: React.ReactNode } & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type={type}
      className={cn('inline-flex items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-bold shadow-card transition hover:brightness-110 disabled:opacity-50', TONES[tone], className)}
      {...rest}
    >
      {icon}
      {children}
    </button>
  )
}
