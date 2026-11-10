// A run as Markdown, to paste into a doc, an issue or another AI: the header facts,
// where the time went, the report if the run has one, and every step of the sequence
// diagram as a table row (when, who to whom, what kind, its title and a short excerpt).
// It leaves out raw payloads and HTTP bodies on purpose: the shape of the run, not
// all of its data.
import type { Run } from '@/lib/api'
import { reportMarkdown } from '@/lib/ask'
import type { Content, Step } from '@/features/runs/story'
import type { Split } from '@/features/runs/timing'
import { duration } from '@/ui/time'

export type RunExport = {
  run: Run
  steps: Step[]
  split?: Split
  exchanges: number
  /** How a handle is shown: "Ada", "@acme-buyer". */
  name: (handle: string) => string
}

const EXCERPT = 140

/** The whole run as Markdown. */
export function runMarkdown({ run, steps, split, exchanges, name }: RunExport): string {
  const start = steps[0]?.at ?? Date.parse(run.startedAt)
  const end = Math.max(start, ...steps.map((s) => s.at), Date.parse(run.updatedAt))
  const took = split?.segments.length ? split.end - split.start : end - start
  const out: string[] = [`# Run: ${oneLine(run.title) || '(no text)'}`, '']

  const facts: [string, string][] = [
    ['Run', `\`${run.id}\``],
    ['State', stateWord(run.state)],
    ['From', `${name(run.starter)} → ${name(run.recipient)}`],
    ['Started', stamp(start, true)],
    ['Last activity', stamp(end, true)],
    ['Took', duration(took)],
    ['Size', `${steps.length} steps · ${run.tasks} ${run.tasks === 1 ? 'task' : 'tasks'} · ${exchanges} HTTP exchanges`],
  ]
  if (split && split.segments.length) {
    const parts = [`${duration(split.working)} working`]
    if (split.queued) parts.push(`${duration(split.queued)} queued`)
    for (const w of split.waiting) parts.push(`${duration(w.ms)} waiting on ${name(w.who)}`)
    facts.push(['Time', parts.join(' · ')])
  }
  if (run.participants.length) facts.push(['Participants', run.participants.map(name).join(', ')])
  if (run.cost) facts.push(['Cost', money(run.cost, run.currency)])
  if (run.waiting.length) facts.push(['Waiting for', run.waiting.map(name).join(', ')])
  if (run.outcome) facts.push(['Outcome', oneLine(run.outcome)])
  for (const [k, v] of facts) out.push(`- **${k}:** ${v}`)

  if (run.report) out.push('', '## Report', '', reportMarkdown(run.report))

  out.push('', '## Steps', '', '| # | Time | +since start | From → To | Kind | What | Excerpt |', '|---|---|---|---|---|---|---|')
  for (const s of steps) {
    const who = s.to ? `${name(s.from)} → ${name(s.to)}` : name(s.from)
    const kind = s.state && s.kind !== 'done' && s.kind !== 'failed' ? `${s.kind} (${stateWord(s.state)})` : s.kind
    let excerpt = excerptOf(s.body)
    if (s.reply) excerpt = [excerpt, `↩ ${excerptOf(s.reply)}`].filter(Boolean).join(' ')
    if (s.updates.length) excerpt = [excerpt, `(+${s.updates.length} ${s.updates.length === 1 ? 'update' : 'updates'})`].filter(Boolean).join(' ')
    if (s.pushes.length) excerpt = [excerpt, `(${s.pushes.length} webhook ${s.pushes.length === 1 ? 'push' : 'pushes'})`].filter(Boolean).join(' ')
    out.push(`| ${s.n} | ${stamp(s.at, false)} | +${duration(s.at - start)} | ${cell(who)} | ${cell(kind)} | ${cell(oneLine(s.title))} | ${cell(clip(excerpt))} |`)
  }
  return out.join('\n') + '\n'
}

/** A file name for the run: "run-order-200-hoodies-01a1c50d.md". */
export function runFileName(run: Run): string {
  const slug = oneLine(run.title)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .replace(/-+$/, '')
  return `run-${slug ? slug + '-' : ''}${run.id.replace(/[^A-Za-z0-9]/g, '').slice(0, 8)}.md`
}

function excerptOf(c: Content | undefined): string {
  if (!c) return ''
  if (c.text) return oneLine(c.text)
  return (c.fields ?? []).map(([k, v]) => `${k}=${oneLine(v)}`).join(', ')
}

function stateWord(s: string): string {
  return s.replace(/^TASK_STATE_/, '').toLowerCase().replace(/_/g, ' ')
}

const oneLine = (s: string | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()

function clip(s: string): string {
  return s.length > EXCERPT ? s.slice(0, EXCERPT - 1).trimEnd() + '…' : s
}

/** A table cell: no pipes or newlines to break the row. */
const cell = (s: string) => s.replace(/\\/g, '\\\\').replace(/\|/g, '\\|')

const pad = (n: number, w = 2) => String(n).padStart(w, '0')

/** Local time with milliseconds; with the date when withDate. */
function stamp(t: number, withDate: boolean): string {
  const d = new Date(t)
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
  if (!withDate) return time
  const off = -d.getTimezoneOffset()
  const tz = `UTC${off >= 0 ? '+' : '−'}${pad(Math.floor(Math.abs(off) / 60))}:${pad(Math.abs(off) % 60)}`
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time} (${tz})`
}

/** "$0.42", or "0.42 EUR" for another currency. */
function money(amount: number, currency?: string): string {
  return !currency || currency === 'USD' ? `$${amount.toFixed(2)}` : `${amount.toFixed(2)} ${currency}`
}
