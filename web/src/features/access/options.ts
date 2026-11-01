import type { Access } from '@/lib/api'

export const ACCESS: { access: Access; label: string }[] = [
  { access: 'workspace', label: 'Workspace' },
  { access: 'connections', label: 'Connections' },
  { access: 'public', label: 'Anyone' },
]

/** What an access setting means, for an agent or a person. */
export function accessHint(access: Access, person = false): string {
  const it = person ? 'you' : 'it'
  switch (access) {
    case 'public':
      return `Anyone can send ${it} a task, even without a token.`
    case 'workspace':
      return `Only agents and people of ${person ? 'your workspaces' : 'its workspace'} can send ${it} tasks.`
    case 'connections':
      return `${person ? 'Your workspaces' : 'Its workspace'}, plus workspaces whose request to connect was accepted.`
  }
}
