import { AlertTriangle, Lightbulb, Pencil, RotateCw } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, type RunDetail } from '@/lib/api'
import { cn } from '@/lib/cn'
import { originalRequest, type Diagnosis } from '@/features/runs/diagnose'
import { Button } from '@/ui/AnswerForm'

/** A failed run's cause and what to do about it, with the request ready to send again. */
export function FailureBanner({ d, diag, name, onTraffic }: { d: RunDetail; diag: Diagnosis; name: (h: string) => string; onTraffic: () => void }) {
  const navigate = useNavigate()
  const request = originalRequest(d)
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(request?.text ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const send = async (t: string) => {
    setBusy(true)
    setError('')
    try {
      const { result } = await api.send({ to: d.run.recipient, text: t, data: request?.data })
      navigate('status' in result ? `/runs/${result.id}` : '/runs')
    } catch (e) {
      setError((e as Error).message)
      setBusy(false)
    }
  }

  const answer = diag.source === 'answer'
  return (
    <div className={cn('border-b px-5 py-3 text-sm', answer ? 'border-line bg-surface-2/60' : 'border-bad/20 bg-bad-soft')}>
      <div className="flex items-start gap-3">
        <AlertTriangle size={18} className={cn('mt-0.5 shrink-0', answer ? 'text-muted' : 'text-bad')} />
        <div className="min-w-0 flex-1 space-y-1.5">
          <p className="font-bold">{diag.title}</p>
          <p className="break-words text-[13px] text-ink/80">{diag.cause}</p>
          {diag.hint && (
            <p className="flex items-start gap-1.5 text-[12.5px] text-muted">
              <Lightbulb size={14} className="mt-0.5 shrink-0 text-warn" />
              <span>
                {diag.hint}
                {diag.status && (
                  <>
                    {' '}
                    <button className="font-semibold text-accent" onClick={onTraffic}>
                      See its response
                    </button>
                  </>
                )}
                {diag.source === 'delivery' && diag.address && (
                  <>
                    {' '}
                    <Link to="/agents" className="font-semibold text-accent">
                      Check {name(diag.address)} on Agents
                    </Link>
                  </>
                )}
              </span>
            </p>
          )}
          {request && editing && (
            <textarea className="input mt-1 min-h-16 w-full font-sans" value={text} onChange={(e) => setText(e.target.value)} autoFocus />
          )}
          {error && <p className="text-xs font-semibold text-bad">{error}</p>}
        </div>
        {request && (
          <div className="flex shrink-0 flex-col items-end gap-1.5">
            <Button icon={<RotateCw size={13} />} disabled={busy || !text.trim()} onClick={() => void send(editing ? text : request.text)} title={`Send the same request to ${name(d.run.recipient)} again, as you`}>
              {editing ? 'Send' : 'Send again'}
            </Button>
            {!editing && (
              <button className="flex items-center gap-1 text-xs font-semibold text-muted hover:text-ink" onClick={() => setEditing(true)}>
                <Pencil size={11} /> Edit first
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
