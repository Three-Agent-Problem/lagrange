/** A handle suggested from a name or email: lowercase letters, digits and dashes, 2-40 long (directory.HandlePattern). */
export function suggestHandle(hint: string): string {
  const base = hint.split('@')[0].toLowerCase()
  const h = base
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .replace(/-+$/, '')
  return h.length >= 2 ? h : ''
}

export const HANDLE_PATTERN = /^[a-z0-9][a-z0-9-]{1,39}$/

/** The parts of an emailed link path: /auth/<purpose>/<token>. */
export function authLinkOf(path: string): { purpose: 'verify' | 'login' | 'reset' | 'invite'; token: string } | undefined {
  const m = /^\/auth\/(verify|login|reset|invite)\/([^/]+)\/?$/.exec(path)
  return m ? { purpose: m[1] as 'verify' | 'login' | 'reset' | 'invite', token: m[2] } : undefined
}
