export function ago(t: number | string, now = Date.now()): string {
  const ms = now - (typeof t === 'string' ? Date.parse(t) : t)
  if (ms < 5_000) return 'just now'
  if (ms < 60_000) return `${Math.round(ms / 1000)}s ago`
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m ago`
  if (ms < 86_400_000) return `${Math.round(ms / 3_600_000)}h ago`
  return new Date(t).toLocaleDateString()
}

export function duration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
  return `${Math.floor(ms / 3_600_000)}h ${Math.round((ms % 3_600_000) / 60_000)}m`
}

export const clock = (t: number | string) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
