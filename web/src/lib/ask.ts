// Multiple-choice questions (lagrange/ask) and situation reports (lagrange/report), as
// the Go packages render them, so the inbox, Telegram and terminals agree.
import type { AskQuestion, Report } from './api'

/** Picks option o of question q: a single-choice question keeps one pick. */
export function toggle(qs: AskQuestion[], picks: number[][], q: number, o: number): number[][] {
  const next = qs.map((_, i) => [...(picks[i] ?? [])])
  if (!qs[q] || o < 0 || o >= qs[q].options.length) return next
  const cur = next[q]
  if (cur.includes(o)) next[q] = cur.filter((x) => x !== o)
  else if (qs[q].multi_select) next[q] = [...cur, o].sort((a, b) => a - b)
  else next[q] = [o]
  return next
}

/** The answer text, one line per question (ask.Format). */
export function formatAnswers(qs: AskQuestion[], picks: number[][], other: string[] = []): string {
  return qs
    .map((q, i) => {
      const head = q.header || q.question
      const labels = (picks[i] ?? []).filter((p) => p >= 0 && p < q.options.length).map((p) => q.options[p].label)
      const note = (other[i] ?? '').trim()
      if (!labels.length && !note) return `${head}: (no answer, your call)`
      if (!labels.length) return `${head}: ${note}`
      if (!note) return `${head}: ${labels.join(', ')}`
      return `${head}: ${labels.join(', ')} (note: ${note})`
    })
    .join('\n')
}

/** A report as Markdown, to paste anywhere (report.Markdown). */
export function reportMarkdown(r: Report): string {
  const out = [`**${r.headline.trim()}**`]
  const line = [r.status, r.progress && r.progress.total > 0 ? `${r.progress.done}/${r.progress.total} done` : ''].filter(Boolean).join(' · ')
  if (line) out.push(line)
  if (r.outcome) out.push('', r.outcome.trim())
  if (r.built?.length) out.push('', '**Built**', ...r.built.map((b) => (b.detail ? `- ${b.title} · ${b.detail}` : `- ${b.title}`)))
  if (r.stats?.length) out.push('', r.stats.map((s) => `${s.label}: ${s.value}`).join(' · '))
  return out.join('\n')
}
