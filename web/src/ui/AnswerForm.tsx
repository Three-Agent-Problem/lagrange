import { Check, Send, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { api, type JSONSchema } from '@/lib/api'
import { cn } from '@/lib/cn'
import { formData, formFields } from '@/lib/schemaForm'

type Props = {
  taskId: string
  /** "person": the task is addressed to me; "sender": the agent asked me back. */
  mode: 'person' | 'sender'
  /** A structured input request, when the agent sent a schema. */
  schema?: JSONSchema
  compact?: boolean
  onDone?: () => void
}

/** Answers a task that waits for input: approve or reject, a structured form, or text. */
export function AnswerForm({ taskId, mode, schema, compact, onDone }: Props) {
  const [text, setText] = useState('')
  const [values, setValues] = useState<Record<string, string | boolean>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const fields = schema ? formFields(schema) : []

  const submit = async (answer: { text?: string; data?: Record<string, unknown>; decision?: 'reject' }) => {
    setBusy(true)
    setError('')
    try {
      await api.answer(taskId, answer)
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
