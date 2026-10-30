import { applyNodeChanges, Background, BackgroundVariant, Controls, MarkerType, MiniMap, ReactFlow, useNodesInitialized, useReactFlow, type NodeChange } from '@xyflow/react'
import { Bot, Hourglass, MessageCircleQuestion, Radio, Rewind, Sparkles } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { cn } from '@/lib/cn'
import { useLive, useView } from '@/store/live'
import { activityOf, flows, isActive, isWaiting, OUTSIDE } from '@/store/model'
import { addressMap, useSession } from '@/store/session'
import { AddressNode, NODE_H, NODE_W, type AddressFlowNode } from '@/features/graph/AddressNode'
import { edgeTypes, type FlowEdgeT } from '@/features/graph/FlowEdge'
import { Feed } from '@/features/graph/Feed'
import { Inspector } from '@/features/graph/Inspector'
import { layout } from '@/features/graph/layout'
import { ReplayBar } from '@/features/graph/ReplayBar'

const nodeTypes = { address: AddressNode }
const FIT = { padding: 0.3, maxZoom: 1.1 }

/** Refits the view when addresses join or leave (the fitView prop only fits once). */
function AutoFit({ shape }: { shape: string }) {
  const { fitView } = useReactFlow()
  const ready = useNodesInitialized()
  useEffect(() => {
    if (ready) void fitView({ ...FIT, duration: 400 })
  }, [ready, shape, fitView])
  return null
}

/** Ticks while mounted, so edge effects (comets, flashes) decay in real time. */
function useNow(ms: number) {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms])
  return now
}

export function LiveGraph() {
  const model = useView()
  const replay = useLive((s) => s.replay)
  const status = useLive((s) => s.status)
  const { addresses, user } = useSession()
  const navigate = useNavigate()
  const wall = useNow(250)
  const now = replay ? model.now : Math.max(wall, model.now)
  const [selected, setSelected] = useState<string>()

  const byHandle = useMemo(() => addressMap(addresses), [addresses])
  const fl = useMemo(() => flows(model), [model])

  // Every address is a node, plus "outside clients" once anonymous A2A callers show up.
  const handles = useMemo(() => {
    const hs = new Set(addresses.map((a) => a.handle))
    for (const f of fl) {
      hs.add(f.source)
      hs.add(f.target)
    }
    return [...hs].sort()
  }, [addresses, fl])

  const layoutKey = handles.join(',') + '|' + fl.map((f) => f.id).sort().join(',')
  const positions = useMemo(
    () => layout(handles.map((id) => ({ id, width: NODE_W, height: NODE_H })), fl.map((f) => ({ source: f.source, target: f.target }))),
    [layoutKey],
  )

  const focus = useMemo(() => {
    if (!selected) return undefined
    const s = new Set([selected])
    for (const f of fl) {
      if (f.source !== selected && f.target !== selected) continue
      s.add(f.source)
      s.add(f.target)
    }
    return s
  }, [selected, fl])

  const derived: AddressFlowNode[] = useMemo(
    () =>
      handles.map((handle) => ({
        id: handle,
        type: 'address',
        position: positions[handle] ?? { x: 0, y: 0 },
        selected: handle === selected,
        style: focus && !focus.has(handle) ? { opacity: 0.35 } : undefined,
        data: {
          handle,
          address: byHandle[handle],
          act: activityOf(model, handle),
          presence: model.presence[handle],
          me: user?.handle,
          readOnly: !!replay,
          onOpenTask: (id: string) => navigate(`/runs/${id}?task=${id}`),
        },
      })),
    [handles, positions, selected, focus, byHandle, model, user, replay, navigate],
  )

  // React Flow measures nodes through onNodesChange; keep its measurements and the
  // positions people dragged, and take everything else from the model.
  const [nodes, setNodes] = useState<AddressFlowNode[]>([])
  const dragged = useRef(new Set<string>())
  useEffect(() => {
    setNodes((prev) => {
      const old = new Map(prev.map((n) => [n.id, n]))
      return derived.map((d) => {
        const p = old.get(d.id)
        if (!p) return d
        return { ...d, measured: p.measured, position: dragged.current.has(d.id) ? p.position : d.position }
      })
    })
  }, [derived])
  const onNodesChange = (changes: NodeChange<AddressFlowNode>[]) => {
    for (const c of changes) if (c.type === 'position' && c.dragging) dragged.current.add(c.id)
    setNodes((ns) => applyNodeChanges(changes, ns))
  }

  const edges: FlowEdgeT[] = fl.map((f) => {
    const a = positions[f.source]
    const b = positions[f.target]
    const back = !!a && !!b && b.x <= a.x
    const color = f.waiting ? 'var(--warn)' : f.active ? 'var(--accent)' : 'var(--muted)'
    return {
      id: f.id,
      source: f.source,
      target: f.target,
      sourceHandle: back ? 'out-back' : 'out',
      targetHandle: back ? 'in-back' : 'in',
      type: 'flow',
      data: { flow: f, now, back, dim: !!focus && !(focus.has(f.source) && focus.has(f.target)) },
      markerEnd: { type: MarkerType.ArrowClosed, color, width: 16, height: 16 },
      zIndex: f.active || f.waiting ? 1 : 0,
    }
  })

  const tasks = Object.values(model.tasks)
  const inFlight = tasks.filter((t) => isActive(t.state)).length
  const waitingPeople = tasks.filter((t) => isWaiting(t.state)).length
  const online = addresses.filter((a) => a.kind === 'agent' && (a.endpoint || (model.presence[a.handle]?.sessions ?? a.stats.sessions) > 0)).length
  const agents = addresses.filter((a) => a.kind === 'agent').length

  return (
    <div className="relative flex h-full min-h-0">
      <div className="relative min-w-0 flex-1">
        <ReactFlow<AddressFlowNode, FlowEdgeT>
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onNodeClick={(_, n) => setSelected(n.id === selected ? undefined : n.id)}
          onPaneClick={() => setSelected(undefined)}
          nodesConnectable={false}
          fitView
          fitViewOptions={FIT}
          minZoom={0.2}
          proOptions={{ hideAttribution: true }}
        >
          <Background variant={BackgroundVariant.Dots} gap={18} size={1.4} color="var(--grid)" />
          <AutoFit shape={handles.join(',')} />
          <Controls showInteractive={false} position="bottom-left" />
          <MiniMap pannable zoomable position="bottom-right" nodeColor={(n) => (n.id === OUTSIDE ? 'var(--line)' : 'var(--accent-soft)')} nodeStrokeColor="var(--accent)" />
        </ReactFlow>

        <div className="pointer-events-none absolute top-4 left-4 flex flex-wrap items-center gap-2">
          <span
            className={cn(
              'pointer-events-auto flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-bold shadow-card ring-1',
              replay ? 'bg-accent text-accent-ink ring-accent' : status === 'live' ? 'bg-surface text-good ring-good/30' : 'bg-surface text-warn ring-warn/30',
            )}
          >
            {replay ? <Rewind size={12} /> : <Radio size={12} className={status === 'live' ? 'animate-pulse' : ''} />}
            {replay ? 'Replay' : status === 'live' ? 'Live' : 'Connecting…'}
          </span>
          <Stat icon={<Sparkles size={12} />} label="in flight" value={inFlight} tone="accent" />
          <Stat icon={<MessageCircleQuestion size={12} />} label="waiting on someone" value={waitingPeople} tone="warn" />
          <Stat icon={<Bot size={12} />} label={`of ${agents} agents online`} value={online} tone="good" />
        </div>

        {agents === 0 && !replay && (
          <div className="absolute top-1/2 left-1/2 w-[360px] -translate-x-1/2 -translate-y-1/2 card p-5 text-center">
            <Hourglass className="mx-auto mb-2 text-accent" />
            <h2 className="text-base font-bold">No agents yet</h2>
            <p className="mt-1 text-sm text-muted">Create an agent address, run the tunnel command next to your agent, and it shows up here, live.</p>
            <Link to="/agents" className="mt-3 inline-block rounded-lg bg-accent px-3 py-1.5 text-sm font-bold text-accent-ink">
              Add an agent
            </Link>
          </div>
        )}
        {replay && <ReplayBar />}
      </div>
      <aside className="hidden w-[340px] shrink-0 border-l border-line bg-surface lg:flex lg:flex-col">
        {selected ? <Inspector handle={selected} address={byHandle[selected]} onClose={() => setSelected(undefined)} /> : <Feed />}
      </aside>
    </div>
  )
}

function Stat({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: number; tone: 'accent' | 'warn' | 'good' }) {
  const color = { accent: 'text-accent', warn: 'text-warn', good: 'text-good' }[tone]
  return (
    <span className="pointer-events-auto flex items-center gap-1.5 rounded-full bg-surface px-2.5 py-1 text-xs font-semibold text-muted shadow-card ring-1 ring-line">
      <span className={color}>{icon}</span>
      <b className={cn('font-mono', value ? color : '')}>{value}</b> {label}
    </span>
  )
}
