import { describe, expect, it } from 'vitest'
import { formatAnswers, reportMarkdown, toggle } from './ask'
import type { AskQuestion } from './api'

const qs: AskQuestion[] = [
  { header: 'Auth', question: 'How?', options: [{ label: 'OAuth' }, { label: 'Email link' }] },
  { header: 'Scope', question: 'Which?', multi_select: true, options: [{ label: 'API' }, { label: 'CLI' }, { label: 'Web' }] },
]

describe('ask', () => {
  it('toggles single and multi choice', () => {
    let p: number[][] = []
    p = toggle(qs, p, 0, 0)
    p = toggle(qs, p, 0, 1)
    p = toggle(qs, p, 1, 2)
    p = toggle(qs, p, 1, 0)
    p = toggle(qs, p, 1, 1)
    p = toggle(qs, p, 1, 1)
    expect(p).toEqual([[1], [0, 2]])
  })
  it('formats like ask.Format', () => {
    expect(formatAnswers(qs, [[0], [0, 2]], ['', 'CLI later'])).toBe('Auth: OAuth\nScope: API, Web (note: CLI later)')
    expect(formatAnswers(qs, [], ['magic links'])).toBe('Auth: magic links\nScope: (no answer, your call)')
  })
  it('renders a report like report.Markdown', () => {
    const md = reportMarkdown({ headline: 'Rate limits', status: 'landed', progress: { done: 3, total: 3 }, built: [{ title: 'S1', detail: 'sonnet' }], stats: [{ label: 'Cost', value: '$1' }], outcome: 'Merged.' })
    expect(md).toBe('**Rate limits**\nlanded · 3/3 done\n\nMerged.\n\n**Built**\n- S1 · sonnet\n\nCost: $1')
  })
})
