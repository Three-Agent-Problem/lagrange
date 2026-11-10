import { describe, expect, it } from 'vitest'
import type { Run, RunDetail } from '@/lib/api'
import { runFileName, runMarkdown } from '@/features/runs/export'
import { story } from '@/features/runs/story'
import { timeSplit } from '@/features/runs/timing'
import fixture from './hoodies.fixture.json'

const detail = fixture as unknown as RunDetail
const name = (h: string) => (h === 'ada' ? 'Ada' : `@${h}`)

const run: Run = {
  id: '01a1c50d-4b0c-72ce-b221-7613ab4139f4',
  title: 'Order 200 hoodies | budget 6000 EUR\ndeliver soon',
  starter: 'ada',
  recipient: 'acme-buyer',
  state: 'TASK_STATE_COMPLETED',
  waiting: [],
  participants: ['ada', 'acme-buyer', 'northwind'],
  tasks: 3,
  entries: detail.entries.length,
  startedAt: detail.entries[0].at,
  updatedAt: detail.entries[detail.entries.length - 1].at,
  cost: 0.42,
  report: { headline: '200 hoodies', status: 'ordered', built: [{ title: 'Order placed' }], at: detail.entries[0].at },
}

describe('runMarkdown', () => {
  const s = story(detail, name)
  const md = runMarkdown({ run, steps: s.steps, split: timeSplit(detail.entries, Date.now()), exchanges: detail.exchanges.length, name })
  const rows = md.split('\n').filter((l) => /^\| \d+ \|/.test(l))

  it('has the header facts, the report and a row per step', () => {
    expect(md).toMatch(/^# Run: Order 200 hoodies \| budget 6000 EUR deliver soon\n/)
    expect(md).toContain('- **State:** completed')
    expect(md).toContain('- **From:** Ada → @acme-buyer')
    expect(md).toContain('- **Cost:** $0.42')
    expect(md).toMatch(/- \*\*Time:\*\* .* working .*waiting on Ada/)
    expect(md).toContain('## Report\n\n**200 hoodies**')
    expect(rows).toHaveLength(s.steps.length)
  })

  it('writes each step with its time, parties, kind and title', () => {
    expect(rows[0]).toMatch(/^\| 1 \| \d\d:\d\d:\d\d\.\d{3} \| \+0ms \| Ada → @acme-buyer \| ask( \(\w[\w ]*\))? \| .+ \|/)
    const wait = rows.find((r) => r.includes('| wait'))
    expect(wait).toContain('@acme-buyer → Ada')
    // Every row keeps the table's 8 pipes (escaped pipes don't count).
    for (const r of rows) expect(r.replace(/\\\|/g, '').split('|')).toHaveLength(9)
  })

  it('names the file after the run', () => {
    expect(runFileName(run)).toBe('run-order-200-hoodies-budget-6000-eur-delive-01a1c50d.md')
  })
})
