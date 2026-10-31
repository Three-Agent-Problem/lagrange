// Structured payloads (A2A data parts) rendered for people: labelled fields with money and
// numbers formatted, machine fields (URLs, IDs) set aside, a lone yes/no answer shown as a
// decision. Pure, so the inbox, the decision page, the feed and the graph agree.
import type { Part } from '@/lib/api'

/** Metadata keys of parts meant for machines: structured input (schema, answers), a credential challenge. */
const MACHINE_PARTS = ['https://github.com/a2aproject/experimental-ext-structured-input/tree/main/v1', 'https://threeagents.dev/ext/credential-challenge/v1']

const isSchema = (p: Part) => p.mediaType === 'application/schema+json' || (p.metadata?.[MACHINE_PARTS[0]] as { type?: string } | undefined)?.type === 'input_schema'

/** The data parts of a message meant for people. A structured answer stays: it is what the person said. */
export function humanData(parts: Part[] | undefined): unknown[] {
  return (parts ?? []).filter((p) => p.data !== undefined && !isSchema(p) && p.metadata?.[MACHINE_PARTS[1]] === undefined).map((p) => p.data)
}

/** A labelled value; nested objects keep their data to render as fields of their own. */
export type Field = { key: string; label: string; value: string; strong?: boolean; nested?: unknown }

const ACRONYMS: Record<string, string> = { id: 'ID', url: 'URL', uri: 'URI', po: 'PO', eur: 'EUR', usd: 'USD' }

/** "shipDate" → "Ship date", "unit_price" → "Unit price", "quoteId" → "Quote ID". */
export function humanize(key: string): string {
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((w) => ACRONYMS[w.toLowerCase()] ?? w.toLowerCase())
  const s = words.join(' ')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

const MONEY = /(total|amount|price|cost|budget|fee|subtotal)$/i
const STRONG = /^(total|amount|grand ?total)$/i
const MACHINE = /(url|uri|id)$/i
/** Fields people look at first, in this order; the rest keep the agent's order. */
const FIRST = ['item', 'product', 'description', 'quantity', 'supplier', 'vendor', 'customer', 'total', 'amount', 'price', 'unitPrice', 'shipDate', 'deliverBy', 'dueDate', 'status']

/** Formats one value; money fields carry the object's currency. */
export function formatValue(key: string, v: unknown, currency?: string): string {
  if (v === null || v === undefined || v === '') return '—'
  if (typeof v === 'boolean') return v ? 'Yes' : 'No'
  if (typeof v === 'number') {
    const n = v.toLocaleString('en-US', { maximumFractionDigits: 2 })
    return currency && MONEY.test(key) ? `${n} ${currency}` : n
  }
  if (typeof v === 'string') return v
  if (Array.isArray(v) && v.every((x) => typeof x !== 'object' || x === null)) return v.map((x) => formatValue('', x)).join(', ')
  if (typeof v === 'object' && !Array.isArray(v)) {
    return Object.entries(v as Record<string, unknown>)
      .map(([k, x]) => `${humanize(k)}: ${formatValue(k, x)}`)
      .join(' · ')
  }
  return JSON.stringify(v)
}

const isObject = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)

/** The fields of an object: what people need (main) and the rest (more), or undefined for non-objects. */
export function dataFields(v: unknown): { main: Field[]; more: Field[] } | undefined {
  if (!isObject(v)) return undefined
  const currency = typeof v.currency === 'string' ? v.currency : undefined
  const hasMoney = !!currency && Object.entries(v).some(([k, x]) => typeof x === 'number' && MONEY.test(k))
  const main: Field[] = []
  const more: Field[] = []
  for (const [key, x] of Object.entries(v)) {
    const f: Field = { key, label: humanize(key), value: formatValue(key, x, currency) }
    if (STRONG.test(key)) f.strong = true
    if (isObject(x) || (Array.isArray(x) && x.some(isObject))) f.nested = x
    const machine = MACHINE.test(key) || (typeof x === 'string' && /^https?:\/\//.test(x)) || (key === 'currency' && hasMoney)
    ;(machine ? more : main).push(f)
  }
  const rank = (f: Field) => {
    const i = FIRST.indexOf(f.key)
    return i < 0 ? FIRST.length : i
  }
  main.sort((a, b) => rank(a) - rank(b))
  return { main, more }
}

/** A lone yes/no answer, such as {"approved": true} from an approval form. */
export function decisionOf(v: unknown): { yes: boolean; label: string } | undefined {
  if (!isObject(v)) return undefined
  const entries = Object.entries(v)
  if (entries.length !== 1 || typeof entries[0][1] !== 'boolean') return undefined
  const [key, yes] = entries[0] as [string, boolean]
  const label = humanize(key)
  return { yes, label: yes ? label : `Not ${label.toLowerCase()}` }
}

/** The complete "key": value pairs of a JSON object cut short (the activity log shortens long payloads). */
function partialObject(t: string): Record<string, unknown> | undefined {
  const out: Record<string, unknown> = {}
  for (const m of t.matchAll(/"([^"\\]+)":("(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?|true|false|null)(?=[,}])/g)) {
    try {
      out[m[1]] = JSON.parse(m[2])
    } catch {
      /* skip */
    }
  }
  return Object.keys(out).length ? out : undefined
}

/** One line for feeds and callouts: a JSON object becomes its first fields, other text stays. */
export function summarize(s: string | undefined, max = 3): string {
  const t = (s ?? '').trim()
  if (!t.startsWith('{')) return t
  let v: unknown
  try {
    v = JSON.parse(t)
  } catch {
    v = partialObject(t)
    if (!v) return t
  }
  const d = decisionOf(v)
  if (d) return `${d.yes ? '✓' : '✗'} ${d.label}`
  const f = dataFields(v)
  if (!f || f.main.length === 0) return t
  return f.main
    .slice(0, max)
    .map((x) => `${x.label} ${x.value}`)
    .join(' · ')
}
