/** Where the agent lives, which picks how the tunnel serves the address. */
export type Mode = 'echo' | 'to' | 'acp' | 'exec' | 'http'

/** The tunnel command for a mode, with the real token in it. */
export function tunnelCommand(server: string, token: string, mode: Mode): string {
  const base = `lagrange tunnel --server ${server} --token ${token}`
  return {
    echo: `${base} --echo`,
    to: `${base} --to http://localhost:9001`,
    acp: `${base} --acp claude-agent-acp --dir ~/code/project`,
    exec: `${base} --exec "python agent.py" --name "My agent"`,
    http: `${base} --http https://n8n.example.com/webhook/my-agent --name "My workflow"`,
  }[mode]
}
