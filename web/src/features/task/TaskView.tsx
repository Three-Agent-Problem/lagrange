import { Background, BackgroundVariant, Controls, Handle, MarkerType, Position, ReactFlow, type Node, type NodeProps } from '@xyflow/react'
import { ArrowLeft, ArrowUpRight, Ban, Package } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, dataOf, textOf, type Entry, type Message, type TaskView as View } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useLive } from '@/store/live'
import { fold, isActive, isWaiting, normState, OUTSIDE, timeInStates, type State, type TaskNode } from '@/store/model'
import { addressMap, useSession } from '@/store/session'
import { edgeTypes, type FlowEdgeT } from '@/features/graph/FlowEdge'
import { layout } from '@/features/graph/layout'
import { AnswerForm, Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { STATE_COLOR, StateBadge } from '@/ui/StateBadge'
import { clock, duration } from '@/ui/time'

type Loaded = { views: Record<string, View>; activity: Entry[]; rootId: string }

/** Loads a task and everything delegated from it (depth-first, a few levels deep). */
async function loadTree(id: string): Promise<Loaded> {
  const views: Record<string, View> = {}
  const activity: Entry[] = []
  const visit = async (tid: string, depth: number) => {
    if (views[tid] || depth > 5) return
    const r = await api.task(tid)
    views[tid] = r.task
    activity.push(...r.activity)
    await Promise.all(r.children.map((c) => visit(c.taskId, depth + 1)))
  }
  await visit(id, 0)
  activity.sort((a, b) => a.seq - b.seq)
  return { views, activity, rootId: id }
}

const CARD_W = 248
const CARD_H = 104

type TaskCardData = { node: TaskNode; view?: View; now: number; selected: boolean; kind: (h: string) => 'agent' | 'human' | undefined }
type TaskCardNode = Node<TaskCardData, 'task'>

function TaskCard({ data }: NodeProps<TaskCardNode>) {
  const { node: t, now, selected } = data
  const spans = timeInStates(t, now)
  const total = spans.reduce((s, x) => s + x.ms, 0) || 1
  return (
    <div
      className={cn('relative rounded-2xl border-2 bg-surface p-2.5 shadow-card', selected ? 'border-accent' : 'border-line', isWaiting(t.state) && 'pulse-ring')}
      style={{ width: CARD_W, height: CARD_H, ['--ring' as string]: STATE_COLOR[t.state] }}
    >
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
      {t.state === 'working' && <span className="work-halo" />}
      <div className="flex items-center gap-2">
        <Avatar handle={t.recipient} kind={data.kind(t.recipient)} size={28} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[12.5px] font-bold">@{t.recipient}</div>
          <div className="truncate font-mono text-[10px] text-muted">from {t.sender === OUTSIDE ? 'outside' : `@${t.sender}`}</div>
        </div>
        <StateBadge state={t.state} />
      </div>
      <p className="mt-1.5 truncate text-[11.5px] text-muted" title={t.summary}>
        {t.summary || '—'}
      </p>
      <div className="mt-1.5 flex h-1.5 overflow-hidden rounded-full bg-surface-2" title={spans.map((s) => `${s.state}: ${duration(s.ms)}`).join('\n')}>
        {spans.map((s, i) => (
          <span key={i} style={{ width: `${(s.ms / total) * 100}%`, background: STATE_COLOR[s.state] }} />
        ))}
      </div>
      <div className="mt-0.5 flex justify-between font-mono text-[9.5px] text-muted">
        <span>{duration(total)}</span>
        {t.artifacts > 0 && (
          <span className="flex items-center gap-0.5">
            <Package size={9} /> {t.artifacts}
          </span>
        )}
      </div>
    </div>
  )
}

const nodeTypes = { task: TaskCard }

export function TaskView() {
  const { id = '' } = useParams()
  const [data, setData] = useState<Loaded>()
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<string>()
  const { addresses, user } = useSession()
  const byHandle = useMemo(() => addressMap(addresses), [addresses])
  const kind = useCallback((h: string) => byHandle[h]?.kind, [byHandle])
  const seq = useLive((s) => s.model.seq)
  const [now, setNow] = useState(Date.now)

  const reload = useCallback(() => {
    loadTree(id)
      .then((d) => (setData(d), setError('')))
      .catch((e: Error) => setError(e.message))
  }, [id])

  useEffect(() => {
    setSelected(undefined)
    reload()
  }, [reload])
  // Follow live changes: refetch shortly after new activity.
  useEffect(() => {
    if (!data) return
    const t = setTimeout(reload, 250)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seq])
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 500)
    return () => clearInterval(t)
  }, [])

  const model = useMemo(() => (data ? fold(data.activity) : undefined), [data])
  const ids = data ? Object.keys(data.views) : []
  const nodesById = useMemo(() => {
    const out: Record<string, TaskNode> = {}
    if (!data || !model) return out
    for (const [tid, v] of Object.entries(data.views)) {
      const m = model.tasks[tid]
      out[tid] = m ? { ...m, state: normState(v.state), summary: m.summary || v.summary } : { ...fallback(v) }
    }
    return out
  }, [data, model])

  const links = ids.filter((t) => nodesById[t]?.parent && nodesById[nodesById[t].parent!]).map((t) => ({ source: nodesById[t].parent!, target: t }))
  const pos = useMemo(() => layout(ids.map((t) => ({ id: t, width: CARD_W, height: CARD_H })), links, { rankSep: 110, nodeSep: 40 }), [ids.join(','), links.map((l) => l.target).join(',')]) // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <div className="p-8 text-sm text-bad">{error}</div>
  if (!data) return <div className="p-8 text-sm text-muted">Loading…</div>

  const nodes: TaskCardNode[] = ids.map((t) => ({
    id: t,
    type: 'task',
    position: pos[t] ?? { x: 0, y: 0 },
    data: { node: nodesById[t], view: data.views[t], now, selected: (selected ?? data.rootId) === t, kind },
  }))
  const edges: FlowEdgeT[] = links.map((l) => {
    const child = nodesById[l.target]
    const active = isActive(child.state) ? 1 : 0
    return {
      id: `${l.source}>${l.target}`,
      source: l.source,
      target: l.target,
      type: 'flow',
      markerEnd: { type: MarkerType.ArrowClosed, color: STATE_COLOR[child.state], width: 16, height: 16 },
      data: {
        now,
        flow: {
          id: l.target,
          source: l.source,
          target: l.target,
          active,
          waiting: isWaiting(child.state) ? 1 : 0,
          queued: child.queued ? 1 : 0,
          done: child.state === 'completed' ? 1 : 0,
          failed: child.state === 'failed' ? 1 : 0,
          total: 1,
          latest: child,
          startedAt: child.createdAt,
          finishedAt: isActive(child.state) || isWaiting(child.state) ? 0 : child.updatedAt,
          finishedState: child.state,
          label: child.summary.slice(0, 28),
        },
      },
    }
  })

  const focus = selected ?? data.rootId
  const view = data.views[focus]
  const root = data.views[data.rootId]
  return (
    <div className="flex h-full min-h-0">
      <div className="relative min-w-0 flex-1">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodeClick={(_, n) => setSelected(n.id)}
          nodesConnectable={false}
          nodesDraggable={false}
          fitView
          fitViewOptions={{ padding: 0.35, maxZoom: 1.15 }}
          proOptions={{ hideAttribution: true }}
        >
          <Background variant={BackgroundVariant.Dots} gap={18} size={1.4} color="var(--grid)" />
          <Controls showInteractive={false} position="bottom-left" />
        </ReactFlow>
        <div className="absolute top-4 left-4 flex items-center gap-2">
          <Link to="/" className="flex items-center gap-1 rounded-full bg-surface px-2.5 py-1 text-xs font-bold text-muted shadow-card ring-1 ring-line hover:text-ink">
            <ArrowLeft size={12} /> Live graph
          </Link>
          {root.parentTaskId && (
            <Link to={`/tasks/${root.parentTaskId}`} className="flex items-center gap-1 rounded-full bg-surface px-2.5 py-1 text-xs font-bold text-accent shadow-card ring-1 ring-line">
              <ArrowUpRight size={12} /> Part of a bigger task
            </Link>
          )}
          <span className="rounded-full bg-surface px-2.5 py-1 font-mono text-xs text-muted shadow-card ring-1 ring-line">{ids.length} task(s) in this tree</span>
        </div>
      </div>
      <aside className="scrollbar-thin w-[400px] shrink-0 overflow-y-auto border-l border-line bg-surface">
        <Details view={view} activity={data.activity.filter((e) => e.taskId === focus)} me={user?.handle} admin={user?.admin} onChange={reload} kind={kind} />
      </aside>
    </div>
  )
}

function fallback(v: View): TaskNode {
  const state = normState(v.state)
  const at = Date.parse(v.updatedAt)
  return { id: v.id, sender: v.sender || OUTSIDE, recipient: v.recipient, parent: v.parentTaskId, state, summary: v.summary, last: '', queued: false, artifacts: v.task?.artifacts?.length ?? 0, createdAt: Date.parse(v.createdAt), updatedAt: at, states: [{ state, at }] }
}

function Details({ view, activity, me, admin, onChange, kind }: { view: View; activity: Entry[]; me?: string; admin?: boolean; onChange: () => void; kind: (h: string) => 'agent' | 'human' | undefined }) {
  const state = normState(view.state) as State
  const task = view.task
  const forMe = isWaiting(state) && ((view.awaiting && view.awaiting === me) || (!view.awaiting && view.sender === me))
  const canCancel = (isActive(state) || isWaiting(state)) && (view.sender === me || view.recipient === me || admin)
  const messages: Message[] = [...(task?.history ?? [])]
  if (task?.status.message && !messages.some((m) => m.messageId === task.status.message!.messageId)) messages.push(task.status.message)
  return (
    <div className="space-y-4 p-4">
      <div>
        <div className="flex items-center gap-2">
          <StateBadge state={state} />
          <span className="ml-auto font-mono text-[10px] text-muted" title={view.id}>
            {view.id.slice(0, 8)}…
          </span>
        </div>
        <h2 className="mt-2 text-base leading-snug font-bold">{view.summary || 'Task'}</h2>
        <p className="mt-1 text-xs text-muted">
          {view.sender ? `@${view.sender}` : 'An outside client'} → @{view.recipient}
        </p>
      </div>

      {forMe && (
        <div className="rounded-xl bg-warn-soft p-3">
          <p className="mb-2 text-xs font-bold text-warn">{view.awaiting === me ? 'This waits for your answer' : `@${view.recipient} asks you`}</p>
          {view.awaiting !== me && task?.status.message && <p className="mb-2 text-sm">{textOf(task.status.message.parts)}</p>}
          <AnswerForm taskId={view.id} mode={view.awaiting === me ? 'person' : 'sender'} schema={view.input?.schema} onDone={onChange} />
        </div>
      )}
      {view.challenge && (
        <a href={view.challenge.url} target="_blank" rel="noreferrer" className="block rounded-xl bg-auth-soft p-3 text-sm font-bold text-auth">
          Connect {view.challenge.integration} to continue →
        </a>
      )}

      <section>
        <h3 className="mb-2 text-[11px] font-bold tracking-wide text-muted uppercase">Conversation</h3>
        <div className="chat-wall space-y-2 rounded-xl p-2.5">
          {messages.map((m) => (
            <Bubble key={m.messageId} m={m} sender={view.sender} recipient={view.recipient} kind={kind} />
          ))}
          {messages.length === 0 && <p className="text-xs text-muted">No messages.</p>}
        </div>
      </section>

      {(task?.artifacts?.length ?? 0) > 0 && (
        <section>
          <h3 className="mb-2 text-[11px] font-bold tracking-wide text-muted uppercase">Artifacts</h3>
          <div className="space-y-2">
            {task!.artifacts!.map((a) => (
              <div key={a.artifactId} className="rounded-xl border border-line p-2.5">
                <div className="mb-1 flex items-center gap-1 text-xs font-bold">
                  <Package size={12} className="text-accent" /> {a.name || a.artifactId}
                </div>
                {textOf(a.parts) && <p className="text-xs whitespace-pre-wrap">{textOf(a.parts)}</p>}
                {dataOf(a.parts) !== undefined && <pre className="scrollbar-thin mt-1 max-h-48 overflow-auto rounded-lg bg-surface-2 p-2 font-mono text-[10.5px]">{JSON.stringify(dataOf(a.parts), null, 2)}</pre>}
              </div>
            ))}
          </div>
        </section>
      )}

      <section>
        <h3 className="mb-2 text-[11px] font-bold tracking-wide text-muted uppercase">Timeline</h3>
        <ol className="relative space-y-2 border-l-2 border-line pl-3">
          {activity.map((e) => (
            <li key={e.seq} className="relative text-xs">
              <span className="absolute top-1 -left-[19px] h-2.5 w-2.5 rounded-full ring-2 ring-surface" style={{ background: e.state ? STATE_COLOR[normState(e.state)] : 'var(--line)' }} />
              <div className="flex items-center gap-1.5">
                <span className="font-mono text-[10px] text-muted">{clock(e.at)}</span>
                {e.state ? <StateBadge state={e.state} /> : <span className="font-semibold text-muted">{e.kind}</span>}
              </div>
              {e.summary && <p className="mt-0.5 text-muted">{e.summary}</p>}
            </li>
          ))}
        </ol>
      </section>

      {canCancel && (
        <Button tone="bad" icon={<Ban size={13} />} onClick={() => void api.cancel(view.id).then(onChange)}>
          Cancel task
        </Button>
      )}
    </div>
  )
}

export function Bubble({ m, sender, recipient, kind, mine }: { m: Message; sender: string; recipient: string; kind: (h: string) => 'agent' | 'human' | undefined; mine?: boolean }) {
  const fromSender = m.role === 'ROLE_USER'
  const who = fromSender ? sender || OUTSIDE : recipient
  const answeredBy = m.metadata?.['https://threeagents.dev/ext/answered-by/v1'] as string | undefined
  const isMine = mine ?? fromSender
  const data = dataOf(m.parts)
  return (
    <div className={cn('flex items-end gap-1.5', isMine && 'flex-row-reverse')}>
      <Avatar handle={answeredBy ?? who} kind={answeredBy ? 'human' : kind(who)} size={22} />
      <div className={cn('max-w-[85%] rounded-2xl px-3 py-1.5 text-[12.5px] shadow-card', isMine ? 'rounded-br-md bg-bubble-me' : 'rounded-bl-md bg-surface')}>
        {textOf(m.parts) && <p className="whitespace-pre-wrap">{textOf(m.parts)}</p>}
        {data !== undefined && <pre className="scrollbar-thin mt-1 max-h-40 overflow-auto rounded-lg bg-surface-2 p-1.5 font-mono text-[10px]">{JSON.stringify(data, null, 2)}</pre>}
        {answeredBy && <p className="mt-0.5 text-[10px] font-semibold text-muted">answered by @{answeredBy}</p>}
      </div>
    </div>
  )
}
