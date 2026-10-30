import { ExternalLink, MessageSquare, X } from 'lucide-react'
import { Link } from 'react-router'
import type { Address } from '@/lib/api'
import { useView } from '@/store/live'
import { OUTSIDE } from '@/store/model'
import { Avatar } from '@/ui/Avatar'
import { Copy } from '@/ui/Copy'
import { StateBadge } from '@/ui/StateBadge'
import { ago } from '@/ui/time'

export function Inspector({ handle, address, onClose }: { handle: string; address?: Address; onClose: () => void }) {
  const model = useView()
  const tasks = Object.values(model.tasks)
    .filter((t) => t.sender === handle || t.recipient === handle)
    .sort((a, b) => b.updatedAt - a.updatedAt)
    .slice(0, 40)
  const presence = model.presence[handle] ?? address?.stats
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-start gap-3 border-b border-line p-4">
        <Avatar handle={handle} kind={address?.kind} name={address?.displayName} size={44} />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-bold">{handle === OUTSIDE ? 'Outside clients' : address?.displayName || address?.card.name || handle}</h2>
          <p className="font-mono text-xs text-muted">{handle === OUTSIDE ? 'A2A callers without a token' : `@${handle}`}</p>
        </div>
        <button className="rounded-lg p-1 text-muted hover:bg-surface-2" onClick={onClose} aria-label="Close">
          <X size={16} />
        </button>
      </div>
      {address && (
        <div className="space-y-3 border-b border-line p-4 text-xs">
          <p className="text-muted">{address.card.description}</p>
          {address.card.skills.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {address.card.skills.map((s) => (
                <span key={s.id} className="rounded-full bg-accent-soft px-2 py-0.5 font-semibold text-accent" title={s.description}>
                  {s.name}
                </span>
              ))}
            </div>
          )}
          {address.kind === 'agent' && !address.endpoint && (
            <p className="font-mono text-muted">
              {presence?.sessions ?? 0} worker(s) · capacity {presence?.capacity ?? 0} · {presence?.inFlight ?? 0} running · {presence?.waiting ?? 0} queued
            </p>
          )}
          <div className="flex items-center gap-1">
            <Link to={`/inbox/${handle}`} className="inline-flex items-center gap-1 rounded-lg bg-accent px-2.5 py-1 font-bold text-accent-ink">
              <MessageSquare size={12} /> Message
            </Link>
            <a href={address.cardUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 rounded-lg px-2 py-1 font-semibold text-muted hover:bg-surface-2">
              <ExternalLink size={12} /> Agent card
            </a>
            <Copy text={address.cardUrl} />
          </div>
        </div>
      )}
      <h3 className="px-4 pt-3 pb-1 text-[11px] font-bold tracking-wide text-muted uppercase">Tasks</h3>
      <ul className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
        {tasks.length === 0 && <li className="px-4 py-2 text-xs text-muted">No tasks yet.</li>}
        {tasks.map((t) => (
          <li key={t.id}>
            <Link to={`/runs/${t.id}?task=${t.id}`} className="block px-4 py-2 hover:bg-surface-2">
              <div className="flex items-center gap-2 text-xs">
                <span className="truncate font-semibold">{t.sender === handle ? `→ @${t.recipient}` : `← ${t.sender === OUTSIDE ? 'outside' : '@' + t.sender}`}</span>
                <StateBadge state={t.state} className="ml-auto" />
              </div>
              <p className="mt-0.5 truncate text-xs text-muted">{t.summary}</p>
              <p className="font-mono text-[10px] text-muted">{ago(t.updatedAt, model.now || Date.now())}</p>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}
