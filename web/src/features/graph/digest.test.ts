import { describe, expect, it } from 'vitest'
import type { Entry, Run } from '@/lib/api'
import type { TaskNode } from '@/store/model'
import { digest, digestLine, headline, runOf } from '@/features/graph/digest'

const now = Date.parse('2026-10-31T12:00:00Z')
const at = (min: number) => new Date(now - min * 60_000).toISOString()
const run = (r: Partial<Run>): Run => ({ id: 'r', title: 'Order 200 hoodies', starter: 'ada', recipient: 'acme-buyer', state: 'TASK_STATE_COMPLETED', waiting: [], participants: [], tasks: 3, entries: 9, startedAt: at(10), updatedAt: at(5), ...r })
const name = (h: string) => (h === 'ada' ? 'Ada' : `@${h}`)

describe('digest', () => {
  it('counts what ended in the window, and everything still open', () => {
    const d = digest(
      [
        run({}),
        run({ state: 'TASK_STATE_FAILED' }),
        run({ state: 'TASK_STATE_COMPLETED', updatedAt: at(120) }),
        run({ state: 'TASK_STATE_WORKING', waiting: ['ada'], updatedAt: at(300) }),
        run({ state: 'TASK_STATE_WORKING' }),
        run({ state: 'message' }),
      ],
      now - 3_600_000,
    )
    expect(d).toMatchObject({ done: 2, failed: 1, waiting: 1, running: 1 })
    expect(digestLine(d, name)).toBe('2 done, 1 failed · 1 waiting (Ada) · 1 in progress')
  })
})

describe('headline', () => {
  it('says who asked whom and what came of it', () => {
    expect(headline(run({ outcome: 'PO-1: 200 hoodies, approved by ada' }), name, now)).toEqual({ who: 'Ada → @acme-buyer', result: 'PO-1: 200 hoodies, approved by ada' })
    expect(headline(run({ state: 'TASK_STATE_REJECTED', outcome: '{"approved":false,"note":"Too much"}' }), name, now).result).toBe('Rejected: approved: false, note: Too much')
    expect(headline(run({ state: 'TASK_STATE_WORKING', waiting: ['ada'], waitingSince: at(12) }), name, now).result).toBe('Waiting on Ada for 12m.')
    expect(headline(run({ state: 'TASK_STATE_WORKING', tasks: 3 }), name, now).result).toBe('In progress: 2 hand-offs so far.')
  })
})

describe('runOf', () => {
  it('walks up to the root task, and a plain message is its own run', () => {
    const tasks = { child: { parent: 'root' }, root: {} } as unknown as Record<string, TaskNode>
    expect(runOf({ seq: 1, at: at(0), kind: 'task', taskId: 'child' } as Entry, tasks)).toBe('root')
    expect(runOf({ seq: 7, at: at(0), kind: 'message', parentTaskId: 'child' } as Entry, tasks)).toBe('root')
    expect(runOf({ seq: 8, at: at(0), kind: 'message' } as Entry, tasks)).toBe('msg-8')
    expect(runOf({ seq: 9, at: at(0), kind: 'presence' } as Entry, tasks)).toBeUndefined()
  })
})
