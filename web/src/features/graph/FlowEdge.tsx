import { BaseEdge, EdgeLabelRenderer, getBezierPath, getSmoothStepPath, type Edge, type EdgeProps } from '@xyflow/react'
import { Hourglass, Pause } from 'lucide-react'
import type { Flow } from '@/store/model'

export type FlowEdgeData = {
  flow: Flow
  /** The clock effects are measured against: wall time live, replay time in replays. */
  now: number
  /** The edge runs right to left, so it is routed under the nodes. */
  back?: boolean
  dim?: boolean
}
export type FlowEdgeT = Edge<FlowEdgeData, 'flow'>

const COMET_MS = 1400
const FLASH_MS = 1800

/**
 * A live flow of tasks between two addresses (neet-syst's FlowEdge): packets run along it
 * while tasks are in flight, more and faster with more tasks; dashed while tasks wait in
 * the mailbox; amber while the recipient waits on someone; a comet when a task starts and
 * a green (or red) flash when one finishes.
 */
export function FlowEdge(props: EdgeProps<FlowEdgeT>) {
  const { id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data, markerEnd, selected } = props
  const flow = data!.flow
  const now = data!.now
  const ends = { sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition }
  const [path, lx, ly] = data?.back ? getSmoothStepPath({ ...ends, borderRadius: 24, offset: 36 }) : getBezierPath(ends)

  const active = flow.active - flow.queued
  const dots = active > 0 ? Math.min(5, active) : 0
  const dur = Math.max(0.7, 1.7 - active * 0.2)
  const waiting = flow.waiting > 0
  const sinceStart = now - flow.startedAt
  const sinceFinish = now - flow.finishedAt
  const comet = sinceStart >= 0 && sinceStart < COMET_MS
  const flash = sinceFinish >= 0 && sinceFinish < FLASH_MS && flow.active === 0
  const flashColor = flow.finishedState === 'completed' ? 'var(--good)' : 'var(--bad)'
  const live = active > 0 || waiting || flow.queued > 0
  const color = waiting ? 'var(--warn)' : active > 0 ? 'var(--accent)' : flow.queued ? 'var(--muted)' : 'var(--muted)'

  return (
    <>
      <BaseEdge
        id={id}
        path={path}
        markerEnd={markerEnd}
        style={{
          stroke: selected ? 'var(--accent)' : color,
          strokeWidth: live ? 1.8 + Math.min(4, flow.active * 0.8) : 1.5,
          strokeDasharray: flow.queued > 0 && active <= 0 ? '6 5' : waiting && !active ? '2 5' : undefined,
          opacity: data?.dim ? 0.2 : live || selected ? 1 : 0.45,
          transition: 'stroke 300ms, stroke-width 300ms, opacity 300ms',
        }}
        interactionWidth={20}
      />
      {comet && <path key={`c${flow.startedAt}`} d={path} fill="none" stroke="var(--accent)" strokeWidth={4} strokeLinecap="round" className="edge-comet" pathLength={1000} />}
      {flash && <path key={`f${flow.finishedAt}`} d={path} fill="none" stroke={flashColor} className="edge-flash" pointerEvents="none" />}
      {Array.from({ length: dots }, (_, i) => (
        <circle key={i} r={4} fill="var(--accent)" stroke="var(--surface)" strokeWidth={1.5}>
          <animateMotion dur={`${dur}s`} repeatCount="indefinite" path={path} begin={`${(i * dur) / dots}s`} />
        </circle>
      ))}
      {waiting && (
        // A slow amber pulse that sits at the recipient: someone has to answer.
        <circle r={5} fill="var(--warn)">
          <animateMotion dur="2.4s" repeatCount="indefinite" path={path} keyPoints="0;0.92;0.92" keyTimes="0;0.6;1" calcMode="linear" />
        </circle>
      )}
      <EdgeLabelRenderer>
        <div className="nodrag nopan pointer-events-none absolute flex items-center gap-1" style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)`, opacity: data?.dim ? 0.3 : 1 }}>
          {(live || selected) && flow.label && (
            <span className="max-w-[160px] truncate rounded-full bg-surface px-2 py-0.5 text-[10px] font-semibold text-muted shadow-card ring-1 ring-line">
              {flow.label}
              {flow.active > 1 && <b className="ml-1 font-mono text-accent">×{flow.active}</b>}
            </span>
          )}
          {flow.queued > 0 && (
            <span className="flex items-center gap-0.5 rounded-full bg-surface-2 px-1.5 py-0.5 font-mono text-[9.5px] font-bold text-muted ring-1 ring-line">
              <Hourglass size={9} /> {flow.queued}
            </span>
          )}
          {waiting && (
            <span className="flex items-center gap-0.5 rounded-full bg-warn-soft px-1.5 py-0.5 font-mono text-[9.5px] font-bold text-warn ring-1 ring-warn/30">
              <Pause size={9} /> {flow.waiting}
            </span>
          )}
          {!live && !selected && flow.total > 0 && <span className="rounded-full bg-surface/80 px-1.5 py-0.5 font-mono text-[9px] text-muted">{flow.total}</span>}
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

export const edgeTypes = { flow: FlowEdge }
