import { Check, ChevronDown, X } from 'lucide-react'
import { useState } from 'react'
import { cn } from '@/lib/cn'
import { dataFields, decisionOf, formatValue } from '@/lib/data'

/**
 * A data part for people: a lone yes/no as a decision, an object as labelled fields
 * (machine fields such as URLs and IDs behind "Details"), anything else as plain values.
 */
export function DataView({ data, className }: { data: unknown; className?: string }) {
  const [open, setOpen] = useState(false)
  const decision = decisionOf(data)
  if (decision) return <Decision {...decision} className={className} />
  const f = dataFields(data)
  if (!f) {
    const items = Array.isArray(data) ? data : [data]
    return (
      <div className={cn('flex flex-col gap-1.5', className)}>
        {items.map((x, i) =>
          dataFields(x) ? <DataView key={i} data={x} /> : <p key={i} className="text-[12.5px]">{formatValue('', x)}</p>,
        )}
      </div>
    )
  }
  return (
    <div className={cn('rounded-xl border border-line bg-surface-2/70 px-3 py-2', className)}>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[12.5px]">
        {f.main.map((x) => (
          <Row key={x.key} label={x.label} value={x.value} strong={x.strong} nested={x.nested} />
        ))}
        {open && f.more.map((x) => <Row key={x.key} label={x.label} value={x.value} nested={x.nested} muted />)}
      </dl>
      {f.more.length > 0 && (
        <button type="button" className="mt-1 flex items-center gap-0.5 text-[10.5px] font-semibold text-muted hover:text-ink" onClick={(e) => (e.stopPropagation(), setOpen(!open))}>
          <ChevronDown size={11} className={cn('transition', open && 'rotate-180')} />
          {open ? 'Fewer details' : `Details (${f.more.length})`}
        </button>
      )}
    </div>
  )
}

function Row({ label, value, strong, muted, nested }: { label: string; value: string; strong?: boolean; muted?: boolean; nested?: unknown }) {
  return (
    <>
      <dt className="text-muted">{label}</dt>
      <dd className={cn('min-w-0 break-words', strong && 'font-bold text-ink', muted && 'font-mono text-[11px] text-muted')}>
        {nested !== undefined ? <DataView data={nested} className="bg-surface/70" /> : value}
      </dd>
    </>
  )
}

/** "✓ Approved" / "✗ Not approved". */
export function Decision({ yes, label, className }: { yes: boolean; label: string; className?: string }) {
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[12px] font-bold', yes ? 'bg-good/15 text-good' : 'bg-bad-soft text-bad', className)}>
      {yes ? <Check size={13} strokeWidth={3} /> : <X size={13} strokeWidth={3} />}
      {label}
    </span>
  )
}

/** Compact key/value chips with the raw keys, for the developer views (Runs). */
export function DataChips({ fields }: { fields: [string, string][] }) {
  return (
    <div className="flex flex-wrap gap-1">
      {fields.map(([k, v]) => (
        <span key={k} className="max-w-full truncate rounded-md bg-accent-soft px-1.5 py-0.5 font-mono text-[10.5px]" title={`${k}: ${v}`}>
          <span className="text-muted">{k}</span> <b className="text-ink">{v}</b>
        </span>
      ))}
    </div>
  )
}
