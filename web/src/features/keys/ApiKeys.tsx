import { KeyRound, Terminal, Trash2 } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { api, type APIKey, type Role } from '@/lib/api'
import { useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { CodeLine } from '@/ui/Copy'
import { ago } from '@/ui/time'
import { defaultKeyRole, keyRoles } from './keys'

/** Settings → API keys: personal keys for scripts, CI and MCP clients, each in one workspace with at most a role. */
export function ApiKeys() {
  const { workspaces, workspace, publicUrl } = useSession()
  const [keys, setKeys] = useState<APIKey[]>()
  const [name, setName] = useState('')
  const [ws, setWs] = useState(workspace?.slug ?? '')
  const mine = workspaces.find((w) => w.slug === ws)?.role
  const [role, setRole] = useState<Role | undefined>(defaultKeyRole(mine))
  const [secret, setSecret] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const refresh = () => api.apiKeys().then((r) => setKeys(r.keys))
  useEffect(() => {
    void refresh()
  }, [])
  useEffect(() => setRole(defaultKeyRole(mine)), [mine])

  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (!role) return
    setBusy(true)
    setError('')
    try {
      const r = await api.createAPIKey({ name: name.trim(), workspace: ws, role })
      setSecret(r.secret)
      setName('')
      await refresh()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const revoke = async (k: APIKey) => {
    if (!window.confirm(`Revoke “${k.name}”? Anything using it stops working.`)) return
    await api.revokeAPIKey(k.id)
    await refresh()
  }

  return (
    <div className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <KeyRound size={17} className="text-accent" />
        <h2 className="font-bold">API keys</h2>
      </div>
      <p className="text-xs text-muted">
        A key acts as you in one workspace, with at most the role you pick, for scripts, CI and MCP clients. Send it as{' '}
        <code className="font-mono">Authorization: Bearer lgk_…</code>. The API is described at{' '}
        <a className="font-semibold text-accent hover:underline" href="/api/openapi.yaml">/api/openapi.yaml</a>.
      </p>
      <div className="flex items-start gap-2 rounded-lg bg-surface-2 px-3 py-2 text-xs text-muted">
        <Terminal size={14} className="mt-0.5 shrink-0" />
        <span>
          On your own computer, <code className="font-mono">lagrange login --server {publicUrl}</code> makes a key through the browser and saves it for you.
        </span>
      </div>

      {secret && (
        <div className="flex flex-col gap-2 rounded-lg border border-good/40 bg-good/5 p-3">
          <p className="text-xs font-semibold">Copy the key now. It isn’t shown again.</p>
          <CodeLine text={secret} />
          <Button onClick={() => setSecret(undefined)}>Done</Button>
        </div>
      )}

      <form onSubmit={create} className="grid gap-3 sm:grid-cols-[1fr_auto_auto_auto] sm:items-end">
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Name
          <input className="input" placeholder="GitHub Actions" required value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Workspace
          <select className="input" value={ws} onChange={(e) => setWs(e.target.value)}>
            {workspaces.map((w) => (
              <option key={w.slug} value={w.slug}>{w.name}</option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Role
          <select className="input" value={role ?? ''} onChange={(e) => setRole(e.target.value as Role)}>
            {keyRoles(mine).map((r) => (
              <option key={r} value={r}>{r}</option>
            ))}
          </select>
        </label>
        <Button type="submit" disabled={busy || !name.trim() || !role}>
          Create key
        </Button>
      </form>
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}

      {keys === undefined ? (
        <div className="h-10 animate-pulse rounded-lg bg-surface-2" />
      ) : keys.length === 0 ? (
        <p className="text-xs text-muted">No keys yet.</p>
      ) : (
        <ul className="divide-y divide-line rounded-lg border border-line">
          {keys.map((k) => (
            <li key={k.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <p className="truncate font-semibold">{k.name}</p>
                <p className="truncate font-mono text-[11px] text-muted">
                  {k.prefix}… · {k.workspace} · {k.role} · {k.lastUsed ? `used ${ago(k.lastUsed)}` : 'never used'}
                </p>
              </div>
              <button type="button" className="rounded-md p-1.5 text-muted hover:bg-surface-2 hover:text-bad" title="Revoke" onClick={() => void revoke(k)}>
                <Trash2 size={14} />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
