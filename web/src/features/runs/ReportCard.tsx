import { ClipboardList } from 'lucide-react'
import type { Report } from '@/lib/api'
import { reportMarkdown } from '@/lib/ask'
import { Copy } from '@/ui/Copy'

/** A run's situation report (lagrange/report): what it is doing, and at the end what it built. */
export function ReportCard({ report }: { report: Report }) {
  const p = report.progress
  return (
    <section className="border-b border-line bg-surface px-4 py-3">
      <div className="flex items-start gap-2">
        <ClipboardList size={16} className="mt-0.5 shrink-0 text-accent" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <h3 className="text-sm font-bold">{report.headline}</h3>
            {report.status && <span className="text-xs font-semibold text-muted">{report.status}</span>}
            {p && p.total > 0 && (
              <span className="text-xs text-muted">
                {p.done}/{p.total} done
              </span>
            )}
          </div>
          {p && p.total > 0 && (
            <div className="mt-1.5 h-1 w-full max-w-sm overflow-hidden rounded-full bg-surface-2">
              <div className="h-full rounded-full bg-good" style={{ width: `${Math.min(100, (p.done / p.total) * 100)}%` }} />
            </div>
          )}
          {report.outcome && <p className="mt-2 text-sm">{report.outcome}</p>}
          {!!report.built?.length && (
            <ul className="mt-2 space-y-0.5 text-sm">
              {report.built.map((b, i) => (
                <li key={i}>
                  • {b.title}
                  {b.detail && <span className="text-xs text-muted"> · {b.detail}</span>}
                </li>
              ))}
            </ul>
          )}
          {!!report.stats?.length && (
            <p className="mt-2 flex flex-wrap gap-x-3 text-xs text-muted">
              {report.stats.map((s) => (
                <span key={s.label}>
                  {s.label} <span className="font-semibold text-ink">{s.value}</span>
                </span>
              ))}
            </p>
          )}
        </div>
        <Copy text={reportMarkdown(report)} label="Copy" />
      </div>
    </section>
  )
}
