/** How the tunnel serves the address: the built-in echo agent, a local A2A agent, or an ACP coding agent. */
export type Mode = 'echo' | 'to' | 'acp'

/** The tunnel command for a mode, with the real token in it. */
export function tunnelCommand(server: string, token: string, mode: Mode): string {
  const base = `lagrange tunnel --server ${server} --token ${token}`
  return { echo: `${base} --echo`, to: `${base} --to http://localhost:9001`, acp: `${base} --acp claude-agent-acp --dir ~/code/project` }[mode]
}
