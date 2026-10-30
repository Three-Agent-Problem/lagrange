import dagre from '@dagrejs/dagre'

export type Box = { id: string; width: number; height: number }
export type Link = { source: string; target: string }

/** Left-to-right layered layout: callers on the left, the agents they delegate to on the right. */
export function layout(nodes: Box[], links: Link[], opts: { rankSep?: number; nodeSep?: number } = {}): Record<string, { x: number; y: number }> {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', ranksep: opts.rankSep ?? 140, nodesep: opts.nodeSep ?? 70, marginx: 40, marginy: 60 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of nodes) g.setNode(n.id, { width: n.width, height: n.height })
  for (const l of links) if (l.source !== l.target && g.hasNode(l.source) && g.hasNode(l.target)) g.setEdge(l.source, l.target)
  dagre.layout(g)
  const out: Record<string, { x: number; y: number }> = {}
  for (const n of nodes) {
    const p = g.node(n.id)
    out[n.id] = { x: p.x - n.width / 2, y: p.y - n.height / 2 }
  }
  return out
}
