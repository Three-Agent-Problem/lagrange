import type { Role, Workspace } from '@/lib/api'

const rank: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 }

/** Whether a role has at least the power of min. */
export function atLeast(role: Role | undefined, min: Role): boolean {
  return !!role && rank[role] >= rank[min]
}

/** The workspace to act in: the one picked last if you are still in it, else your oldest. */
export function pickWorkspace(all: Workspace[], saved: string): Workspace | undefined {
  return all.find((w) => w.slug === saved) ?? all[0]
}

/** Two letters for a workspace's badge. */
export function initials(name: string): string {
  const words = name.trim().split(/[\s\-_]+/).filter(Boolean)
  const s = words.length > 1 ? words[0][0] + words[1][0] : (words[0] ?? '?').slice(0, 2)
  return s.toUpperCase()
}
