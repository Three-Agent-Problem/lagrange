import { describe, expect, it } from 'vitest'
import type { Exchange, RunDetail, TaskView } from '@/lib/api'
import { diagnose, FAILURE, originalRequest } from '@/features/runs/diagnose'

const name = (h: string) => `@${h}`

function task(id: string, state: string, recipient: string, text: string, metadata?: Record<string, unknown>, history?: TaskView['task']): TaskView {
  return {
    id, contextId: 'c', state, sender: 'ada', recipient, summary: '', createdAt: '', updatedAt: '',
    task: history ?? { id, contextId: 'c', status: { state, message: { messageId: 'm', role: 'ROLE_AGENT', parts: [{ text }], metadata } } },
  } as TaskView
}

function run(state: string, tasks: TaskView[], exchanges: Exchange[] = []): RunDetail {
  return { run: { id: tasks[0].id, title: 'Order 200 hoodies', starter: 'ada', recipient: tasks[0].recipient, state, waiting: [], participants: [], tasks: tasks.length, entries: 0, startedAt: '', updatedAt: '' }, tasks, entries: [], exchanges }
}

describe('diagnose', () => {
  it('leaves runs that did not fail alone', () => {
    expect(diagnose(run('TASK_STATE_COMPLETED', [task('t1', 'TASK_STATE_COMPLETED', 'buyer', 'done')]), name)).toBeUndefined()
  })

  it('explains a tunnel whose agent is down, at the deepest failure', () => {
    const d = run('TASK_STATE_FAILED', [
      task('t1', 'TASK_STATE_FAILED', 'buyer', 'No quotes.'),
      task('t2', 'TASK_STATE_FAILED', 'northwind', 'x', { [FAILURE]: { kind: 'agent-down', address: 'northwind', detail: 'nothing answers at http://localhost:9001 (connection refused). Is the agent running there?' } }),
    ])
    const g = diagnose(d, name)!
    expect(g.title).toBe("@northwind isn't running")
    expect(g.cause).toContain('localhost:9001')
    expect(g.hint).toContain('--to')
    expect(g.source).toBe('delivery')
  })

  it('reads the HTTP status of an agent error', () => {
    const d = run('TASK_STATE_FAILED', [task('t1', 'TASK_STATE_FAILED', 'buyer', 'x', { [FAILURE]: { kind: 'agent-error', address: 'buyer', detail: 'bad' } })], [
      { id: 1, at: '', direction: 'out', recipient: 'buyer', method: 'POST', url: 'http://agent/rpc', status: 500, durationMs: 3, request: '', response: '' } as Exchange,
    ])
    expect(diagnose(d, name)!.title).toBe('@buyer hit an error (HTTP 500)')
  })

  it("quotes an agent's own failure and a rejection", () => {
    expect(diagnose(run('TASK_STATE_FAILED', [task('t1', 'TASK_STATE_FAILED', 'buyer', 'No supplier can deliver in time.')]), name)).toMatchObject({
      title: '@buyer gave up', cause: 'No supplier can deliver in time.', source: 'answer',
    })
    expect(diagnose(run('TASK_STATE_REJECTED', [task('t1', 'TASK_STATE_REJECTED', 'buyer', 'Order rejected by ada.')]), name)).toMatchObject({ title: 'Rejected', cause: 'Order rejected by ada.' })
  })
})

describe('originalRequest', () => {
  it('is the first user message of the root task, data included', () => {
    const t = task('t1', 'TASK_STATE_FAILED', 'buyer', '', undefined, {
      id: 't1', contextId: 'c', status: { state: 'TASK_STATE_FAILED' },
      history: [{ messageId: 'u', role: 'ROLE_USER', parts: [{ text: 'Order 200 hoodies, budget 6000 EUR' }, { data: { rush: true } }] }],
    })
    expect(originalRequest(run('TASK_STATE_FAILED', [t]))).toEqual({ text: 'Order 200 hoodies, budget 6000 EUR', data: { rush: true } })
  })
})
