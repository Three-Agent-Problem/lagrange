import { CheckCircle2, ExternalLink, Link2Off } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, ApiError, type Decision as DecisionView } from '@/lib/api'
import { Lockup } from '@/ui/Brand'
import { isWaiting, normState } from '@/store/model'
import { AnswerForm } from '@/ui/AnswerForm'
import { Bubble } from '@/ui/Bubble'
import { StateBadge } from '@/ui/StateBadge'
import { ago } from '@/ui/time'

const AWAITING = 'https://threeagents.dev/ext/awaiting/v1'

/**
 * The page a notification links to (/d/<token>): one question, answerable once without
 * logging in. Approvers often open it on a phone, so it is a single narrow column.
 */
export function Decision({ token }: { token: string }) {
  const [view, setView] = useState<DecisionView>()
  const [error, setError] = useState<{ gone: boolean; message: string }>()
  const [sent, setSent] = useState(false)

  useEffect(() => {
    api.decision(token).then(setView, (e: Error) => setError({ gone: e instanceof ApiError && e.status === 410, message: e.message }))
  }, [token])

  return (
    <div className="chat-wall min-h-full px-4 py-6 sm:py-12">
      <div className="mx-auto flex w-full max-w-lg flex-col gap-4">
        <a href="/" className="flex items-center gap-2 self-start">
          <Lockup size={17} />
        </a>
        {error ? (
          <Notice icon={<Link2Off size={22} className="text-muted" />} title={error.gone ? 'This link no longer works' : 'Something went wrong'} text={error.message} />
        ) : sent && view ? (
          <Notice
            icon={<CheckCircle2 size={22} className="text-good" />}
            title="Sent"
            text={`@${asker(view)} has your answer and carries on. You can close this page.`}
          />
        ) : view ? (
          <Question view={view} token={token} onSent={() => setSent(true)} />
        ) : (
          <div className="card h-48 animate-pulse" />
        )}
      </div>
    </div>
  )
}

/** Who is asking: the sender of a task addressed to the person, else the agent that asked back. */
function asker(v: DecisionView) {
  return v.task.recipient === v.for.handle ? v.task.sender || 'someone' : v.task.recipient
}

function Question({ view, token, onSent }: { view: DecisionView; token: string; onSent: () => void }) {
  const t = view.task
  const me = view.for.handle
  const state = normState(t.state)
  const forPerson = t.recipient === me
  const history = [...(t.task?.history ?? [])]
  const status = t.task?.status.message
  if (status && !history.some((m) => m.messageId === status.messageId) && !status.metadata?.[AWAITING]) history.push(status)
  const kind = (h: string): 'human' | 'agent' => (h === me ? 'human' : 'agent')

  return (
    <div className="card flex flex-col gap-4 p-5">
      <div>
        <h1 className="text-lg leading-tight font-extrabold">@{asker(view)} needs your answer</h1>
        <p className="mt-1 text-xs text-muted">
          For {view.for.name} (@{me}) · asked {ago(t.updatedAt)}
        </p>
      </div>
      <div className="flex flex-col gap-2 rounded-xl bg-surface-2/60 p-3">
        {history.map((m) => (
          <Bubble key={m.messageId} m={m} sender={t.sender} recipient={t.recipient} kind={kind} mine={(m.role === 'ROLE_USER') !== forPerson} />
        ))}
      </div>
      {state === 'input_required' ? (
        <AnswerForm taskId={t.id} mode={forPerson ? 'person' : 'sender'} schema={t.input?.schema} questions={t.questions} approval={t.approval} send={(a) => api.answerDecision(token, a)} onDone={onSent} />
      ) : state === 'auth_required' && t.challenge ? (
        <a href={t.challenge.url} target="_blank" rel="noreferrer" className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-accent px-3 py-2 text-sm font-bold text-accent-ink shadow-card">
          Connect {t.challenge.integration} <ExternalLink size={14} />
        </a>
      ) : (
        <p className="flex items-center gap-2 text-sm text-muted">
          <StateBadge state={state} /> {isWaiting(state) ? 'Waiting on something this page can’t answer.' : 'Nothing left to answer here.'}
        </p>
      )}
      <p className="border-t border-line pt-3 text-[11px] text-muted">
        This link works once and expires {new Date(view.expiresAt).toLocaleDateString()}. Anyone who has it can answer as you, so don’t forward it.{' '}
        <a href="/inbox" className="font-semibold text-accent">
          Open your inbox
        </a>
      </p>
    </div>
  )
}

function Notice({ icon, title, text }: { icon: React.ReactNode; title: string; text: string }) {
  return (
    <div className="card flex flex-col items-start gap-2 p-5">
      {icon}
      <h1 className="text-lg font-extrabold">{title}</h1>
      <p className="text-sm text-muted">{text}</p>
      <a href="/inbox" className="mt-1 text-sm font-semibold text-accent">
        Open your inbox
      </a>
    </div>
  )
}
