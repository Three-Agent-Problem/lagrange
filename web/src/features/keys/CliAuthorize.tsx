import { Terminal } from 'lucide-react'
import { useState } from 'react'
import { AuthShell, Problem, Submit } from '@/features/auth/parts'
import { api, type Role } from '@/lib/api'
import { useSession } from '@/store/session'
import { CodeLine } from '@/ui/Copy'
import { cliRequest, defaultKeyRole, keyRoles } from './keys'

/** /cli/authorize: the browser half of `lagrange login`. You pick the workspace and role; the CLI gets a key. */
export function CliAuthorize() {
  const { user, workspaces, workspace, publicUrl } = useSession()
  const req = cliRequest(location.search)
  const [ws, setWs] = useState(workspace?.slug ?? '')
  const mine = workspaces.find((w) => w.slug === ws)?.role
  const [role, setRole] = useState<Role | undefined>(defaultKeyRole(mine))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [code, setCode] = useState('')
  const [done, setDone] = useState(false)

  if (!req) {
    return (
      <AuthShell title="This login link is broken" subtitle="Run lagrange login again in your terminal.">
        <a href="/" className="text-xs font-semibold text-accent hover:underline">Open Lagrange</a>
      </AuthShell>
    )
  }
  if (done) {
    return (
      <AuthShell title="You’re logged in" subtitle="Go back to your terminal. You can close this tab.">
        <span />
      </AuthShell>
    )
  }
  if (code) {
    return (
      <AuthShell title="Paste this code in your terminal" subtitle="It works once, for the next 5 minutes.">
        <CodeLine text={code} />
      </AuthShell>
    )
  }

  const approve = async () => {
    if (!role) return
    setBusy(true)
    setError('')
    try {
      const r = await api.authorizeCLI({ ...req, workspace: ws, role })
      if (r.redirect) {
        setDone(true)
        location.assign(r.redirect)
      } else setCode(r.code)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell
      title="Log in the Lagrange CLI?"
      subtitle={
        <>
          <span className="font-semibold">{req.name}</span> asks for a key on {new URL(publicUrl).host}. It acts as @{user?.handle} in the workspace you pick, with at most the role you pick.
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          void approve()
        }}
      >
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Workspace
          <select
            className="input"
            value={ws}
            onChange={(e) => {
              setWs(e.target.value)
              setRole(defaultKeyRole(workspaces.find((w) => w.slug === e.target.value)?.role))
            }}
          >
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
        <p className="flex items-start gap-2 text-xs text-muted">
          <Terminal size={14} className="mt-0.5 shrink-0" />
          <span>
            Only approve if you just ran <code className="font-mono">lagrange login</code>. You can revoke the key in Settings → API keys.
          </span>
        </p>
        <Problem text={error} />
        <Submit busy={busy || !role}>Approve</Submit>
      </form>
    </AuthShell>
  )
}
