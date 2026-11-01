import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { Hourglass, KeyRound, Lock, MessageCircleQuestion, Plug, X } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useState } from 'react'
import { api, type Address, type JSONSchema, type Stats } from '@/lib/api'
import { cn } from '@/lib/cn'
import { summarize } from '@/lib/data'
import { OUTSIDE, shorten, type Activity, type TaskNode } from '@/store/model'
import { AnswerForm } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'

export const NODE_W = 208
export const NODE_H = 68

export type AddressNodeData = {
  handle: string
  address?: Address
  act: Activity
  presence?: Stats
  me?: string
  /** Read-only views (replay) don't offer inline answers. */
  readOnly?: boolean
  onOpenTask?: (id: string) => void
}
export type AddressFlowNode = Node<AddressNodeData, 'address'>

type Tone = 'idle' | 'working' | 'waiting' | 'auth' | 'failed' | 'offline'

const RING: Record<Tone, string> = {
  idle: 'border-line',
  working: 'border-accent',
  waiting: 'border-warn pulse-ring',
  auth: 'border-auth pulse-ring',
  failed: 'border-bad',
  offline: 'border-line border-dashed',
}

const RING_COLOR: Partial<Record<Tone, string>> = { waiting: 'var(--warn)', auth: 'var(--auth)' }

function toneOf(d: AddressNodeData): Tone {
  const { act, address, presence } = d
  const waiting = [...act.inbox, ...act.asking]
  if (waiting.some((t) => t.state === 'auth_required')) return 'auth'
  if (waiting.length) return 'waiting'
  if (act.failed) return 'failed'
  if (act.working) return 'working'
  // Another workspace's tunnels aren't shown, so its agents never look offline.
  const tunnelAgent = address?.kind === 'agent' && !address.endpoint && !address.external
  if (tunnelAgent && !(presence?.sessions ?? address.stats.sessions)) return 'offline'
  return 'idle'
}

export function AddressNode({ data, selected }: NodeProps<AddressFlowNode>) {
  const { handle, address, act, me } = data
  const presence = data.presence ?? address?.stats
  const tone = toneOf(data)
  const human = address?.kind === 'human'
  const external = !!address?.external
  const tunnel = address?.kind === 'agent' && !address.endpoint && !external
  const sessions = presence?.sessions ?? 0
  const capacity = presence?.capacity ?? 0
  const inFlight = presence?.inFlight ?? act.working
  const queued = Math.max(presence?.waiting ?? 0, act.queued)
  const name = handle === OUTSIDE ? 'Outside clients' : address?.displayName || address?.card?.name || handle
  const question = act.inbox[0] ?? act.asking[0]

  return (
    <div
      data-focus={selected ? 'selected' : undefined}
      className={cn(
        'addr-node relative flex items-center gap-2.5 rounded-2xl border-2 bg-surface px-3 shadow-card transition-colors',
        RING[tone],
        tone === 'offline' && 'opacity-70',
        external && 'border-dashed',
      )}
      title={external ? `@${handle} is in another workspace: you see the calls between you, not its own work` : undefined}
      style={{ width: NODE_W, height: NODE_H, ['--ring' as string]: RING_COLOR[tone] }}
    >
      <Handle type="target" position={Position.Left} id="in" />
      <Handle type="target" position={Position.Bottom} id="in-back" />
      <Handle type="source" position={Position.Right} id="out" />
      <Handle type="source" position={Position.Bottom} id="out-back" />
      {tone === 'working' && <span className="work-halo" />}

      <AnimatePresence>{question && <Callout key={question.id} task={question} handle={handle} me={me} readOnly={data.readOnly} onOpen={data.onOpenTask} />}</AnimatePresence>

      <Avatar handle={handle} kind={address?.kind} name={address?.displayName} size={38} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] leading-tight font-bold" title={name}>
          {name}
        </div>
        <div className="truncate font-mono text-[10.5px] text-muted">{handle === OUTSIDE ? 'any A2A client' : `@${handle}`}</div>
      </div>

      <span className="absolute -top-2.5 left-3 rounded-full bg-surface-2 px-1.5 py-0.5 font-mono text-[9px] font-bold tracking-wide text-muted uppercase ring-1 ring-line">
        {handle === OUTSIDE ? 'public' : external ? 'external' : human ? 'person' : tunnel ? 'tunnel' : 'endpoint'}
      </span>

      {tunnel && (
        <span
          className={cn(
            'absolute -top-2.5 right-3 flex items-center gap-1 rounded-full px-1.5 py-0.5 font-mono text-[9.5px] font-bold ring-1',
            sessions ? 'bg-good-soft text-good ring-good/30' : 'bg-surface-2 text-muted ring-line',
          )}
          title={sessions ? `${sessions} connected worker${sessions > 1 ? 's' : ''}` : 'Offline: tasks wait in the mailbox'}
        >
          {sessions ? <span className="h-1.5 w-1.5 rounded-full bg-good" /> : <Plug size={10} />}
          {sessions ? `×${sessions}` : 'offline'}
        </span>
      )}
      {human && act.inbox.length > 0 && (
        <span className="absolute -top-2.5 right-3 flex items-center gap-1 rounded-full bg-warn px-1.5 py-0.5 font-mono text-[9.5px] font-bold text-white">
          <MessageCircleQuestion size={10} /> {act.inbox.length}
        </span>
      )}
      {tone === 'failed' && (
        <span className="absolute -top-2.5 -right-2.5 flex h-6 w-6 items-center justify-center rounded-full bg-bad text-white">
          <X size={14} strokeWidth={3} />
        </span>
      )}

      {tunnel && (capacity > 0 || queued > 0) && (
        <div className="absolute right-3 -bottom-2.5 left-3 flex items-center gap-1.5" title="Mailbox: tasks running out of capacity, and tasks waiting">
          <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-surface-2 ring-1 ring-line">
            <div
              className={cn('h-full rounded-full transition-all duration-500', queued ? 'bg-warn' : inFlight ? 'bg-accent' : 'bg-line')}
              style={{ width: `${capacity ? Math.min(100, (inFlight / capacity) * 100) : 100}%` }}
            />
          </div>
          <span className="flex items-center gap-0.5 rounded bg-surface px-1 font-mono text-[9px] font-bold text-muted ring-1 ring-line">
            {queued ? (
              <>
                <Hourglass size={9} /> {queued} queued
              </>
            ) : (
              `${inFlight}/${capacity}`
            )}
          </span>
        </div>
      )}
    </div>
  )
}

/** The pending question, floating above the node. People answer it right here. */
function Callout({ task, handle, me, readOnly, onOpen }: { task: TaskNode; handle: string; me?: string; readOnly?: boolean; onOpen?: (id: string) => void }) {
  const auth = task.state === 'auth_required'
  const forMe = !readOnly && ((task.awaiting === handle && handle === me) || (task.awaiting !== handle && task.sender === me))
  const text = summarize(task.awaiting === handle ? task.summary : task.last || task.summary)
  const schema = useInputSchema(forMe && !auth ? task.id : undefined, task.updatedAt)
  return (
    <motion.div
      initial={{ opacity: 0, y: 8, scale: 0.94 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      exit={{ opacity: 0, y: 4, scale: 0.96 }}
      className={cn(
        'absolute bottom-full left-1/2 z-20 mb-3.5 w-max max-w-[250px] -translate-x-1/2 rounded-xl px-3 py-2 text-[11.5px] leading-snug font-semibold shadow-card',
        auth ? 'bg-auth text-white' : 'bg-warn text-white',
        forMe ? 'pointer-events-auto' : 'pointer-events-none',
      )}
    >
      <div className="mb-0.5 flex items-center gap-1 text-[10px] font-bold tracking-wide uppercase opacity-85">
        {auth ? <KeyRound size={10} /> : <Lock size={10} />}
        {task.awaiting === handle ? task.sender === OUTSIDE ? 'An outside client asks' : `@${task.sender} asks` : `asks @${task.sender === OUTSIDE ? 'its caller' : task.sender}`}
      </div>
      <button type="button" className="nodrag nopan text-left hover:underline" onClick={() => onOpen?.(task.id)}>
        {shorten(text || 'Waiting for an answer', 90)}
      </button>
      {forMe && !auth && (
        <div className="mt-2 rounded-lg bg-surface p-2 text-ink">
          <AnswerForm key={schema ? 'form' : 'text'} taskId={task.id} mode={task.awaiting === handle ? 'person' : 'sender'} schema={schema} compact />
        </div>
      )}
      <span className={cn('absolute top-full left-1/2 -mt-1 h-2 w-2 -translate-x-1/2 rotate-45', auth ? 'bg-auth' : 'bg-warn')} />
    </motion.div>
  )
}

/** The structured input the agent asked for, if any: the live model only carries text. */
function useInputSchema(taskId: string | undefined, version: number): JSONSchema | undefined {
  const [schema, setSchema] = useState<{ id: string; schema?: JSONSchema }>()
  useEffect(() => {
    if (!taskId) return
    let live = true
    api
      .task(taskId)
      .then((r) => live && setSchema({ id: taskId, schema: r.task.input?.schema }))
      .catch(() => {})
    return () => {
      live = false
    }
  }, [taskId, version])
  return schema && schema.id === taskId ? schema.schema : undefined
}

export const nodeTypes = { address: AddressNode }
