import { describe, expect, it } from 'vitest'
import type { Entry } from '@/lib/api'
import { timeSplit } from '@/features/runs/timing'

const t0 = Date.UTC(2026, 9, 31, 9, 0, 0)
let seq = 0
const e = (s: number, p: Partial<Entry> & { kind: Entry['kind'] }): Entry => ({ seq: ++seq, at: new Date(t0 + s * 1000).toISOString(), ...p })

describe('timeSplit', () => {
  it('splits a run into working, queued and waiting on someone', () => {
    seq = 0
    const entries = [
      e(0, { kind: 'task', taskId: 't1', sender: 'ada', recipient: 'buyer', state: 'TASK_STATE_SUBMITTED' }),
      e(1, { kind: 'task', taskId: 't2', sender: 'buyer', recipient: 'supplier', parentTaskId: 't1', state: 'TASK_STATE_SUBMITTED' }),
      e(1, { kind: 'queued', taskId: 't2' }), // the supplier is offline for 4s
      e(5, { kind: 'delivered', taskId: 't2' }),
      e(6, { kind: 'task', taskId: 't2', state: 'TASK_STATE_COMPLETED' }),
      e(7, { kind: 'task', taskId: 't1', state: 'TASK_STATE_INPUT_REQUIRED' }), // asks Ada
      e(37, { kind: 'input', taskId: 't1' }),
      e(37, { kind: 'task', taskId: 't1', state: 'TASK_STATE_WORKING' }),
      e(39, { kind: 'task', taskId: 't1', state: 'TASK_STATE_COMPLETED' }),
      e(99, { kind: 'artifact', taskId: 't1' }),
    ]
    const s = timeSplit(entries, t0 + 500_000)
    expect(s.working).toBe(5000) // 0-1, 5-7, 37-39
    expect(s.queued).toBe(4000)
    expect(s.waiting).toEqual([{ who: 'ada', ms: 30_000 }])
    expect(s.end - s.start).toBe(39_000)
    expect(s.segments.map((x) => x.kind)).toEqual(['working', 'queued', 'working', 'waiting', 'working'])
  })

  it('names who a task awaits, and counts a run still waiting up to now', () => {
    seq = 0
    const entries = [
      e(0, { kind: 'task', taskId: 't1', sender: 'ada', recipient: 'buyer', state: 'TASK_STATE_SUBMITTED' }),
      e(2, { kind: 'task', taskId: 't1', state: 'TASK_STATE_INPUT_REQUIRED', data: { awaiting: 'cfo' } }),
    ]
    const s = timeSplit(entries, t0 + 10_000)
    expect(s.waiting).toEqual([{ who: 'cfo', ms: 8000 }])
    expect(s.working).toBe(2000)
  })
})
