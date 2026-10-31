import { CheckCircle2, ExternalLink, Loader2, Send, X } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { tunnelCommand, type Mode } from '@/features/agents/commands'
import { api, textOf, type A2ATask, type Message } from '@/lib/api'
import { cn } from '@/lib/cn'
import { isTerminal, isWaiting, normState } from '@/store/model'
import { useView } from '@/store/live'
import { useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { CodeLine } from '@/ui/Copy'


const MODES: { id: Mode; label: string; hint: string }[] = [
  { id: 'echo', label: 'Try it now', hint: 'A test agent built into lagrange that repeats what you send. No code needed.' },
  { id: 'to', label: 'Your A2A agent', hint: 'Any A2A agent, in any language, listening on a local port.' },
  { id: 'acp', label: 'Coding agent', hint: 'Claude Code, Gemini CLI, opencode or Codex over ACP. Its “may I run this?” prompts land in your inbox.' },
]

/**
 * Right after an address is created (or a token minted): the command to run with the
 * token filled in, a live check that the tunnel connected, and a test message.
 */
export function Connect({ handle, token, onClose }: { handle: string; token: string; onClose: () => void }) {
  const { publicUrl, addresses, refreshAddresses } = useSession()
  const presence = useView().presence[handle]
  const address = addresses.find((a) => a.handle === handle)
  const [mode, setMode] = useState<Mode>('echo')
  const [answered, setAnswered] = useState(false)
  const connected = !!address?.endpoint || (presence?.sessions ?? address?.stats.sessions ?? 0) > 0
  const workers = presence?.sessions ?? address?.stats.sessions ?? 0
  const cardName = address?.card.name
  // The server stores the agent's card as the tunnel connects: fetch it to show its name.
  useEffect(() => {
    if (connected) void refreshAddresses()
  }, [connected, refreshAddresses])

  return (
    <section className="card relative space-y-5 border-accent/30 p-5 text-sm">
      <button className="absolute top-3 right-3 rounded-lg p-1.5 text-muted hover:bg-surface-2 hover:text-ink" onClick={onClose} title="Close">
        <X size={15} />
      </button>
      <div>
        <h2 className="text-base font-bold">Connect @{handle}</h2>
        <p className="text-xs text-muted">
          Its address is <code className="font-mono text-ink">{publicUrl}/a/{handle}</code>. Three steps, about two minutes.
        </p>
      </div>

      <Step n={1} done={connected} title="Run the tunnel next to your agent">
        <div className="flex flex-wrap gap-1">
          {MODES.map((m) => (
            <button key={m.id} className={cn('rounded-full px-2.5 py-1 text-xs font-bold', mode === m.id ? 'bg-accent text-accent-ink' : 'bg-surface-2 text-muted hover:text-ink')} onClick={() => setMode(m.id)}>
              {m.label}
            </button>
          ))}
        </div>
        <p className="text-xs text-muted">{MODES.find((m) => m.id === mode)!.hint}</p>
        <CodeLine text={tunnelCommand(publicUrl, token, mode)} />
        <p className="text-[11px] text-muted">
          The token is in the command and is shown only now; copy it before you close this. No <code className="font-mono">lagrange</code> yet?{' '}
          <code className="font-mono text-ink">go install github.com/three-agent-problem/lagrange/cmd/lagrange@latest</code>
        </p>
      </Step>

      <Step n={2} done={connected} title={connected ? 'Connected' : 'Waiting for the tunnel…'}>
        {connected ? (
          <p className="text-xs text-good">
            {address?.endpoint ? 'Reached at its public endpoint' : `${workers} worker${workers === 1 ? '' : 's'} online`}
            {cardName && cardName !== handle && (
              <>
                {' '}
                · card: <b>{cardName}</b>
              </>
            )}
          </p>
        ) : (
          <p className="flex items-center gap-1.5 text-xs text-muted">
            <Loader2 size={13} className="animate-spin" /> This turns green by itself when the command connects.
          </p>
        )}
      </Step>

      <Step n={3} done={answered} title="Send a test message" disabled={!connected}>
        <TestMessage handle={handle} disabled={!connected} onAnswered={() => setAnswered(true)} />
      </Step>
    </section>
  )
}

function Step({ n, done, title, disabled, children }: { n: number; done: boolean; title: string; disabled?: boolean; children: React.ReactNode }) {
  return (
    <div className={cn('flex gap-3', disabled && 'opacity-50')}>
      <span className={cn('flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-bold', done ? 'bg-good text-white' : 'bg-accent-soft text-accent')}>
        {done ? <CheckCircle2 size={14} /> : n}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <h3 className="font-bold">{title}</h3>
        {children}
      </div>
    </div>
  )
}

type Sent = { id?: string; state: string; reply: string }

function TestMessage({ handle, disabled, onAnswered }: { handle: string; disabled: boolean; onAnswered: () => void }) {
  const [text, setText] = useState('Hello from Lagrange!')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [sent, setSent] = useState<Sent>()

  // Follow the task until it finishes or waits on someone.
  useEffect(() => {
    if (!sent?.id || isTerminal(normState(sent.state)) || isWaiting(normState(sent.state))) return
    const t = setTimeout(() => {
      void api.task(sent.id!).then(({ task }) => setSent({ id: task.id, state: task.state, reply: replyOf(task.task) }))
    }, 600)
    return () => clearTimeout(t)
  }, [sent])

  const send = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    setSent(undefined)
    try {
      const { result } = await api.send({ to: handle, text })
      if ('status' in result) setSent({ id: result.id, state: result.status.state, reply: replyOf(result) })
      else setSent({ state: 'completed', reply: textOf((result as Message).parts) })
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const state = sent ? normState(sent.state) : undefined
  const settled = state && (isTerminal(state) || isWaiting(state))
  useEffect(() => {
    if (state === 'completed') onAnswered()
  }, [state, onAnswered])
  return (
    <div className="space-y-2">
      <form onSubmit={send} className="flex gap-2">
        <input className="input" value={text} onChange={(e) => setText(e.target.value)} disabled={disabled} />
        <Button type="submit" disabled={disabled || busy || !text.trim()} icon={<Send size={13} />}>
          Send
        </Button>
      </form>
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}
      {sent && (
        <div className={cn('rounded-xl p-3 text-xs', state === 'failed' || state === 'rejected' ? 'bg-bad-soft' : settled ? 'bg-good-soft/60' : 'bg-surface-2')}>
          {!settled ? (
            <p className="flex items-center gap-1.5 text-muted">
              <Loader2 size={13} className="animate-spin" /> Sent. Waiting for @{handle}…
            </p>
          ) : (
            <>
              <p className="font-bold">{state === 'completed' ? `@${handle} answered` : isWaiting(state!) ? `@${handle} asks something back` : `The task ended: ${state}`}</p>
              {sent.reply && <p className="mt-1 whitespace-pre-wrap">{sent.reply}</p>}
            </>
          )}
          <div className="mt-2 flex gap-3 font-semibold">
            <Link to={`/inbox/${handle}`} className="text-accent">
              Open the conversation
            </Link>
            {sent.id && (
              <Link to={`/runs/${sent.id}`} className="flex items-center gap-1 text-accent">
                See what happened, step by step <ExternalLink size={11} />
              </Link>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

/** What the agent said: its status message, else its artifacts' text. */
function replyOf(task?: A2ATask): string {
  if (!task) return ''
  const status = textOf(task.status.message?.parts)
  if (status) return status
  return (task.artifacts ?? []).map((a) => textOf(a.parts)).filter(Boolean).join('\n')
}
