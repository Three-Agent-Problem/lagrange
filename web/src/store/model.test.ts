import { describe, expect, it } from 'vitest'
import type { Entry, Graph } from '@/lib/api'
import { activityOf, flows, fold, fromSnapshot, OUTSIDE, reduce, timeInStates, tree, waitingFor } from '@/store/model'

let seq = 0
const at = (s: number) => new Date(Date.UTC(2026, 9, 30, 12, 0, s)).toISOString()
const e = (p: Partial<Entry> & { kind: Entry['kind'] }, s = seq): Entry => ({ seq: ++seq, at: at(s), ...p })

// A buyer task delegates a quote to a supplier, then waits on Ada's approval.
function procurement(): Entry[] {
  seq = 0
  return [
    e({ kind: 'task', taskId: 't1', sender: 'ada', recipient: 'buyer', state: 'TASK_STATE_SUBMITTED', summary: 'Order 200 hoodies' }, 0),
    e({ kind: 'task', taskId: 't1', sender: 'ada', recipient: 'buyer', state: 'TASK_STATE_WORKING', summary: 'Asking suppliers' }, 1),
    e({ kind: 'queued', taskId: 't2', sender: 'buyer', recipient: 'supplier', parentTaskId: 't1' }, 2),
    e({ kind: 'task', taskId: 't2', sender: 'buyer', recipient: 'supplier', parentTaskId: 't1', state: 'TASK_STATE_SUBMITTED', summary: 'Quote 200 hoodies' }, 2),
    e({ kind: 'delivered', taskId: 't2', sender: 'buyer', recipient: 'supplier', parentTaskId: 't1' }, 3),
    e({ kind: 'artifact', taskId: 't2', sender: 'buyer', recipient: 'supplier', parentTaskId: 't1', summary: 'quote' }, 4),
    e({ kind: 'task', taskId: 't2', sender: 'buyer', recipient: 'supplier', parentTaskId: 't1', state: 'TASK_STATE_COMPLETED' }, 5),
    e({ kind: 'task', taskId: 't3', sender: 'buyer', recipient: 'ada', parentTaskId: 't1', state: 'TASK_STATE_SUBMITTED', summary: 'Approve 5,416 EUR?' }, 6),
    e({ kind: 'task', taskId: 't3', sender: 'buyer', recipient: 'ada', parentTaskId: 't1', state: 'TASK_STATE_INPUT_REQUIRED', summary: 'Waiting for Ada', data: { awaiting: 'ada' } }, 6),
    e({ kind: 'presence', recipient: 'supplier', data: { sessions: 2, capacity: 8, inFlight: 1, waiting: 0 } }, 7),
  ]
}

describe('reduce', () => {
  it('follows tasks through their states', () => {
    const m = fold(procurement())
    expect(m.seq).toBe(10)
    expect(m.tasks.t1.state).toBe('working')
    expect(m.tasks.t1.summary).toBe('Order 200 hoodies')
    expect(m.tasks.t1.last).toBe('Asking suppliers')
    expect(m.tasks.t2).toMatchObject({ state: 'completed', parent: 't1', queued: false, artifacts: 1 })
    expect(m.tasks.t3).toMatchObject({ state: 'input_required', awaiting: 'ada' })
    expect(m.presence.supplier).toEqual({ sessions: 2, capacity: 8, inFlight: 1, waiting: 0 })
  })

  it('ignores entries it has already seen and never mutates', () => {
    const entries = procurement()
    const m = fold(entries)
    const frozen = JSON.stringify(m)
    expect(reduce(m, entries[3])).toBe(m)
    reduce(m, e({ kind: 'task', taskId: 't3', state: 'TASK_STATE_COMPLETED' }, 9))
    expect(JSON.stringify(m)).toBe(frozen)
  })

  it('marks queued tasks until they are delivered', () => {
    const entries = procurement()
    const m = fold(entries.slice(0, 4))
    expect(m.tasks.t2.queued).toBe(true)
    expect(activityOf(m, 'supplier').queued).toBe(1)
    expect(fold(entries.slice(0, 5)).tasks.t2.queued).toBe(false)
  })

  it('replays to the same model as live', () => {
    const entries = procurement()
    const live = entries.reduce(reduce, fold([]))
    expect(fold(entries)).toEqual(live)
  })
})

describe('derived views', () => {
  it('draws one flow per sender and recipient', () => {
    const m = fold(procurement())
    const byId = Object.fromEntries(flows(m).map((f) => [f.id, f]))
    expect(Object.keys(byId).sort()).toEqual(['ada>buyer', 'buyer>ada', 'buyer>supplier'])
    expect(byId['buyer>supplier']).toMatchObject({ done: 1, active: 0, finishedState: 'completed' })
    expect(byId['buyer>ada']).toMatchObject({ waiting: 1 })
    expect(byId['ada>buyer']).toMatchObject({ active: 1, label: 'Order 200 hoodies' })
  })

  it("puts a person's pending questions in their inbox", () => {
    const m = fold(procurement())
    expect(activityOf(m, 'ada').inbox.map((t) => t.id)).toEqual(['t3'])
    expect(activityOf(m, 'buyer')).toMatchObject({ working: 1, blocked: 1 })
    expect(waitingFor(m, 'ada').map((t) => t.id)).toEqual(['t3'])
  })

  it('builds the delegation tree root first', () => {
    expect(tree(fold(procurement()), 't1').map((t) => t.id)).toEqual(['t1', 't2', 't3'])
  })

  it('measures time in each state', () => {
    const m = fold(procurement())
    expect(timeInStates(m.tasks.t2, m.now).map((s) => [s.state, s.ms])).toEqual([
      ['submitted', 3000],
      ['completed', 0],
    ])
  })

  it('starts from the server snapshot', () => {
    const g: Graph = {
      seq: 41,
      addresses: [],
      tasks: [{ id: 'x', contextId: 'c', state: 'TASK_STATE_WORKING', sender: '', recipient: 'echo', summary: 'hi', createdAt: at(0), updatedAt: at(1) }],
    }
    const m = fromSnapshot(g)
    expect(m.tasks.x).toMatchObject({ sender: OUTSIDE, state: 'working' })
    expect(reduce(m, { seq: 41, at: at(2), kind: 'task', taskId: 'x', state: 'TASK_STATE_COMPLETED' })).toBe(m)
    expect(reduce(m, { seq: 42, at: at(2), kind: 'task', taskId: 'x', state: 'TASK_STATE_COMPLETED' }).tasks.x.state).toBe('completed')
  })
})
