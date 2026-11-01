import { useEffect, useRef } from 'react'
import { cn } from '@/lib/cn'
import type { Step, StepKind } from '@/features/runs/story'
import { shorten } from '@/store/model'
import { Avatar } from '@/ui/Avatar'

const LANE_W = 128
const ROW_H = 40
const HEAD_H = 64
const PAD = 28

/** The colour of a step, by what kind of thing happened. */
export const KIND_COLOR: Record<StepKind, string> = {
  ask: 'var(--work)',
  message: 'var(--work)',
  progress: 'var(--muted)',
  artifact: 'var(--work)',
  wait: 'var(--warn)',
  answer: 'var(--good)',
  queued: 'var(--warn)',
  push: 'var(--auth)',
  done: 'var(--good)',
  failed: 'var(--bad)',
}

/** A few words for the arrow: what was asked, or what happened. */
function label(s: Step): string {
  const f = (k: string) => s.body?.fields?.find(([key]) => key === k)?.[1]
  switch (s.kind) {
    case 'ask':
    case 'message':
      return shorten(f('request') ?? f('skill') ?? s.body?.text ?? (s.kind === 'message' ? 'message' : 'task'), 22)
    case 'wait':
      return s.to ? (s.state === 'auth_required' ? 'needs sign-in' : 'asks') : 'waiting'
    case 'answer':
      return shorten(s.body?.text ?? (f('approved') === 'true' ? 'approved' : f('approved') === 'false' ? 'declined' : 'answer'), 22)
    case 'done':
      return 'done'
    case 'failed':
      return s.state ?? 'failed'
    case 'artifact':
      return shorten(s.title.match(/“(.+)”/)?.[1] ?? 'result', 20)
    case 'queued':
      return 'in mailbox'
    default:
      return shorten(s.body?.text ?? '', 22)
  }
}

type Props = {
  steps: Step[]
  lanes: string[]
  /** The step in focus (index), highlighted and scrolled to. */
  focus?: number
  /** While replaying, steps after the focus are hidden. */
  replaying: boolean
  kind: (handle: string) => 'agent' | 'human' | undefined
  name: (handle: string) => string
  onPick: (index: number) => void
}

/** The run as a sequence diagram: one lane per participant, one row per step. */
export function Sequence({ steps, lanes, focus, replaying, kind, name, onPick }: Props) {
  const box = useRef<HTMLDivElement>(null)
  const x = (h: string) => PAD + lanes.indexOf(h) * LANE_W + LANE_W / 2
  const width = PAD * 2 + lanes.length * LANE_W
  const height = steps.length * ROW_H + 16

  useEffect(() => {
    if (focus === undefined || !box.current) return
    const top = focus * ROW_H
    const el = box.current
    if (top < el.scrollTop + 8 || top > el.scrollTop + el.clientHeight - ROW_H * 2) el.scrollTo({ top: Math.max(0, top - el.clientHeight / 2), behavior: 'smooth' })
  }, [focus])

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div ref={box} className="scrollbar-thin min-h-0 flex-1 overflow-auto">
        <div className="sticky top-0 z-10 flex border-b border-line bg-surface" style={{ width, height: HEAD_H, paddingLeft: PAD, paddingRight: PAD }}>
          {lanes.map((h) => (
            <div key={h} className="flex flex-col items-center justify-center gap-1" style={{ width: LANE_W }} title={name(h)}>
              <Avatar handle={h} kind={kind(h)} name={name(h)} size={28} />
              <span className="max-w-full truncate px-1 text-[11px] font-bold">{name(h)}</span>
            </div>
          ))}
        </div>
        <svg width={width} height={height} className="block" role="img" aria-label="Sequence of the run's steps">
          <defs>
            {Object.entries(KIND_COLOR).map(([k, c]) => (
              <marker key={k} id={`seq-arrow-${k}`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M0,0 L10,5 L0,10 z" fill={c} />
              </marker>
            ))}
          </defs>
          {lanes.map((h) => (
            <line key={h} x1={x(h)} x2={x(h)} y1={0} y2={height} stroke="var(--line)" strokeDasharray="3 4" />
          ))}
          {steps.map((s, i) => {
            const y = i * ROW_H + ROW_H / 2 + 4
            const hidden = replaying && focus !== undefined && i > focus
            const active = focus === i
            const color = KIND_COLOR[s.kind]
            const x1 = x(s.from)
            const x2 = s.to ? x(s.to) : x1
            const self = !s.to || x1 === x2
            return (
              <g
                key={s.n}
                className={cn('cursor-pointer transition-opacity duration-300', hidden && 'opacity-10')}
                onClick={() => onPick(i)}
                role="button"
                aria-label={`Step ${s.n}: ${s.title}`}
              >
                <rect x={0} y={y - ROW_H / 2} width={width} height={ROW_H} fill={active ? 'var(--accent-soft)' : 'transparent'} />
                <text x={6} y={y + 4} className="font-mono" fontSize={10} fill={active ? 'var(--accent)' : 'var(--muted)'} fontWeight={700}>
                  {s.n}
                </text>
                {self ? (
                  <>
                    <circle cx={x1} cy={y} r={active ? 6 : 5} fill={color} />
                    <text x={x1 + 10} y={y + 4} fontSize={11} fill="var(--ink)">
                      {label(s)}
                    </text>
                  </>
                ) : (
                  <>
                    <line
                      x1={x1}
                      x2={x2 + (x2 > x1 ? -6 : 6)}
                      y1={s.reply ? y - 5 : y}
                      y2={s.reply ? y - 5 : y}
                      stroke={color}
                      strokeWidth={active ? 2.5 : 1.75}
                      markerEnd={`url(#seq-arrow-${s.kind})`}
                    />
                    {s.reply && (
                      <line x1={x2} x2={x1 + (x1 > x2 ? -6 : 6)} y1={y + 7} y2={y + 7} stroke={color} strokeWidth={1.25} strokeDasharray="4 3" markerEnd={`url(#seq-arrow-${s.kind})`} />
                    )}
                    <text x={(x1 + x2) / 2} y={y - (s.reply ? 9 : 5)} textAnchor="middle" fontSize={10.5} fill="var(--ink)" fontWeight={600}>
                      {label(s)}
                    </text>
                  </>
                )}
                {s.pushes.length > 0 &&
                  [...new Set(s.pushes.map((p) => p.to))].map((to) => (
                    <line
                      key={to}
                      x1={x1}
                      x2={x(to) + (x(to) > x1 ? -6 : 6)}
                      y1={y + 11}
                      y2={y + 11}
                      stroke={KIND_COLOR.push}
                      strokeWidth={1.25}
                      strokeDasharray="2 3"
                      markerEnd="url(#seq-arrow-push)"
                    />
                  ))}
              </g>
            )
          })}
        </svg>
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-line px-3 py-2 text-[10.5px] text-muted">
        <Legend color={KIND_COLOR.ask} label="asks" />
        <Legend color={KIND_COLOR.ask} label="reply" dashed />
        <Legend color={KIND_COLOR.wait} label="waits on someone" />
        <Legend color={KIND_COLOR.answer} label="answer / done" />
        <Legend color={KIND_COLOR.push} label="webhook push" dashed />
        <span className="flex items-center gap-1">
          <span className="h-2 w-2 rounded-full bg-muted" /> inside one agent
        </span>
      </div>
    </div>
  )
}

function Legend({ color, label, dashed }: { color: string; label: string; dashed?: boolean }) {
  return (
    <span className="flex items-center gap-1">
      <svg width="18" height="6" aria-hidden>
        <line x1="0" x2="18" y1="3" y2="3" stroke={color} strokeWidth="2" strokeDasharray={dashed ? '3 2' : undefined} />
      </svg>
      {label}
    </span>
  )
}
