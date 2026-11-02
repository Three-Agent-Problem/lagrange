import type { Role } from '@/lib/api'
import { atLeast } from '@/features/workspaces/roles'

/** The roles a key can get: up to yours, never owner (owners manage members, keys shouldn't). */
export function keyRoles(mine: Role | undefined): Role[] {
  return (['viewer', 'member', 'admin'] as Role[]).filter((r) => atLeast(mine, r))
}

/** The role a new key starts with: member if you can give it, else the highest you can. */
export function defaultKeyRole(mine: Role | undefined): Role | undefined {
  const roles = keyRoles(mine)
  return roles.includes('member') ? 'member' : roles.at(-1)
}

/** What `lagrange login` asks for, from the /cli/authorize link. undefined when the link is broken. */
export type CLIRequest = { challenge: string; name: string; port: number }

export function cliRequest(search: string): CLIRequest | undefined {
  const q = new URLSearchParams(search)
  const challenge = q.get('challenge') ?? ''
  if (!/^[A-Za-z0-9_-]{43}$/.test(challenge)) return undefined
  const rawPort = q.get('port')
  const port = rawPort ? Number(rawPort) : 0
  if (!Number.isInteger(port) || (port !== 0 && (port < 1024 || port > 65535))) return undefined
  return { challenge, name: (q.get('name') ?? '').slice(0, 100) || 'lagrange CLI', port }
}
