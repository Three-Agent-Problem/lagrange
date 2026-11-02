import { ChevronDown, ChevronRight, Pause, Play, RotateCw, Send, Trash2, Webhook as WebhookIcon } from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, type Webhook, type WebhookDelivery } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useCan } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { CodeLine } from '@/ui/Copy'
import { ago } from '@/ui/time'
import { deliveryLine, EVENT_LABELS } from './events'

/** Settings → Webhooks (admins): endpoints that hear about waits, answers, finished runs and agents going on and offline. */
export function Webhooks() {
  const admin = useCan('admin')
  const [hooks, setHooks] = useState<Webhook[]>()
  const [types, setTypes] = useState<string[]>([])
  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [events, setEvents] = useState<string[]>(['task.waiting', 'run.completed', 'run.failed'])
  const [secret, setSecret] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    const r = await api.webhooks()
    setHooks(r.webhooks)
    setTypes(r.events)
  }, [])
  useEffect(() => {
    if (admin) void refresh()
  }, [admin, refresh])
  if (!admin) return null

  const create = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const r = await api.createWebhook({ url: url.trim(), description: description.trim(), events })
      setSecret(r.secret)
      setUrl('')
      setDescription('')
      await refresh()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const toggle = (t: string) => setEvents((ev) => (ev.includes(t) ? ev.filter((x) => x !== t) : [...ev, t]))

  return (
    <div className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <WebhookIcon size={17} className="text-accent" />
        <h2 className="font-bold">Webhooks</h2>
      </div>
      <p className="text-xs text-muted">
        Lagrange POSTs a signed JSON event to your URL when something happens in this workspace, so Zapier, n8n, Make or your own service can react without polling.
        Check the <code className="font-mono">Lagrange-Signature</code> header with the signing secret. Failed deliveries are retried for about a day.
      </p>

      {secret && (
        <div className="flex flex-col gap-2 rounded-lg border border-good/40 bg-good/5 p-3">
          <p className="text-xs font-semibold">Signing secret. Copy it now: it isn’t shown again.</p>
          <CodeLine text={secret} />
          <Button onClick={() => setSecret(undefined)}>Done</Button>
        </div>
      )}

      <form onSubmit={create} className="flex flex-col gap-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
            URL
            <input className="input font-mono" type="url" placeholder="https://hooks.example.com/lagrange" required value={url} onChange={(e) => setUrl(e.target.value)} />
          </label>
          <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
            Description
            <input className="input" placeholder="Approvals to #ops" value={description} onChange={(e) => setDescription(e.target.value)} />
          </label>
        </div>
        <fieldset className="grid gap-1.5 sm:grid-cols-2">
          {types.map((t) => (
            <label key={t} className="flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-0.5 h-4 w-4 accent-[var(--accent)]" checked={events.includes(t)} onChange={() => toggle(t)} />
              <span>
                <code className="font-mono text-xs font-semibold">{t}</code>
                <span className="block text-xs text-muted">{EVENT_LABELS[t] ?? ''}</span>
              </span>
            </label>
          ))}
        </fieldset>
        <div className="flex items-center gap-2">
          <Button type="submit" disabled={busy || !url.trim() || events.length === 0}>
            Add webhook
          </Button>
          {error && <span className="text-xs font-semibold text-bad">{error}</span>}
        </div>
      </form>

      {hooks === undefined ? (
        <div className="h-12 animate-pulse rounded-lg bg-surface-2" />
      ) : hooks.length === 0 ? (
        <p className="text-xs text-muted">No webhooks yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {hooks.map((h) => (
            <HookRow key={h.id} hook={h} onChange={refresh} />
          ))}
        </ul>
      )}
    </div>
  )
}

function HookRow({ hook, onChange }: { hook: Webhook; onChange: () => Promise<void> }) {
  const [open, setOpen] = useState(false)
  const [deliveries, setDeliveries] = useState<WebhookDelivery[]>()
  const [shown, setShown] = useState<number>()
  const load = useCallback(() => api.webhookDeliveries(hook.id).then((r) => setDeliveries(r.deliveries)), [hook.id])
  useEffect(() => {
    if (!open) return
    void load()
    const t = setInterval(() => void load(), 3000)
    return () => clearInterval(t)
  }, [open, load])

  const act = async (f: () => Promise<unknown>) => {
    await f()
    await onChange()
    if (open) await load()
  }
  const remove = () => {
    if (window.confirm(`Delete the webhook to ${hook.url}?`)) void act(() => api.deleteWebhook(hook.id))
  }

  return (
    <li className="rounded-lg border border-line">
      <div className="flex items-center gap-2 px-3 py-2">
        <button type="button" className="rounded p-0.5 text-muted hover:bg-surface-2" onClick={() => setOpen(!open)} title={open ? 'Hide deliveries' : 'Show deliveries'}>
          {open ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
        </button>
        <div className="min-w-0 flex-1">
          <p className={cn('truncate font-mono text-xs font-semibold', !hook.active && 'text-muted line-through')}>{hook.url}</p>
          <p className="truncate text-[11px] text-muted">
            {hook.description && `${hook.description} · `}
            {hook.events.join(', ')}
          </p>
        </div>
        <span className="hidden shrink-0 text-[11px] text-muted sm:block">
          {hook.delivered} ok{hook.failed > 0 && <span className="font-semibold text-bad"> · {hook.failed} failed</span>}
          {hook.pending > 0 && ` · ${hook.pending} pending`} · 24h
        </span>
        <IconButton title="Send a test event" onClick={() => void act(() => api.testWebhook(hook.id).then(() => setOpen(true)))}>
          <Send size={13} />
        </IconButton>
        <IconButton title={hook.active ? 'Pause' : 'Resume'} onClick={() => void act(() => api.updateWebhook(hook.id, { active: !hook.active }))}>
          {hook.active ? <Pause size={13} /> : <Play size={13} />}
        </IconButton>
        <IconButton title="Delete" danger onClick={remove}>
          <Trash2 size={13} />
        </IconButton>
      </div>
      {open && (
        <div className="border-t border-line px-3 py-2">
          {deliveries === undefined ? (
            <div className="h-8 animate-pulse rounded bg-surface-2" />
          ) : deliveries.length === 0 ? (
            <p className="text-xs text-muted">No deliveries yet. Send a test event to try the endpoint.</p>
          ) : (
            <ul className="divide-y divide-line">
              {deliveries.map((d) => (
                <li key={d.id} className="py-1.5 text-xs">
                  <div className="flex items-center gap-2">
                    <span
                      className={cn(
                        'h-2 w-2 shrink-0 rounded-full',
                        d.status === 'delivered' ? 'bg-good' : d.status === 'failed' ? 'bg-bad' : 'bg-warn animate-pulse',
                      )}
                    />
                    <button type="button" className="font-mono font-semibold hover:underline" onClick={() => setShown(shown === d.id ? undefined : d.id)}>
                      {d.type}
                    </button>
                    <span className="min-w-0 flex-1 truncate text-muted">{deliveryLine(d)}</span>
                    <span className="shrink-0 text-muted">{ago(d.createdAt)}</span>
                    {d.status !== 'pending' && (
                      <IconButton title="Send again" onClick={() => void act(() => api.retryDelivery(hook.id, d.id))}>
                        <RotateCw size={12} />
                      </IconButton>
                    )}
                  </div>
                  {shown === d.id && (
                    <pre className="scrollbar-thin mt-1.5 max-h-56 overflow-auto rounded bg-surface-2 p-2 font-mono text-[11px]">{JSON.stringify(d.payload, null, 2)}</pre>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </li>
  )
}

function IconButton({ title, danger, onClick, children }: { title: string; danger?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" title={title} onClick={onClick} className={cn('shrink-0 rounded-md p-1.5 text-muted hover:bg-surface-2', danger ? 'hover:text-bad' : 'hover:text-ink')}>
      {children}
    </button>
  )
}
