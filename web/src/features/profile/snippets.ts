/** Ways to call an address from code, each checked against a running server. */
export type Snippet = { id: 'cli' | 'curl' | 'python' | 'go'; label: string; code: string }

const TOKEN = '$LAGRANGE_TOKEN'

/** Snippets that send "text" to the address at url; with auth, they carry an address token. */
export function snippets(url: string, text: string, auth: boolean): Snippet[] {
  const quoted = JSON.stringify(text)
  const body = JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'SendMessage', params: { message: { messageId: 'msg-1', role: 'ROLE_USER', parts: [{ text }] } } })
  const curl = [`curl -s ${url}/rpc`, `  -H 'Content-Type: application/json' -H 'A2A-Version: 1.0'`]
  if (auth) curl.push(`  -H "Authorization: Bearer ${TOKEN}"`)
  curl.push(`  -d '${body.replaceAll("'", "'\\''")}'`)

  const python = `# pip install "a2a-sdk>=1.3" httpx
import asyncio, ${auth ? 'os, ' : ''}uuid
import httpx
from a2a.client import ClientConfig, create_client
from a2a.types import Message, Part, Role, SendMessageRequest

async def main():
    http = httpx.AsyncClient(timeout=60${auth ? `, headers={"Authorization": "Bearer " + os.environ["LAGRANGE_TOKEN"]}` : ''})
    client = await create_client(${JSON.stringify(url)}, client_config=ClientConfig(httpx_client=http, streaming=False))
    msg = Message(role=Role.ROLE_USER, message_id=str(uuid.uuid4()), parts=[Part(text=${quoted})])
    async for event in client.send_message(SendMessageRequest(message=msg)):
        print(event)

asyncio.run(main())
`

  const go = `// go get github.com/a2aproject/a2a-go/v2${auth ? ' github.com/three-agent-problem/lagrange' : ''}
card, err := agentcard.DefaultResolver.Resolve(ctx, ${JSON.stringify(url)})
if err != nil {
	return err
}
client, err := a2aclient.NewFromCard(ctx, card${auth ? `,
	a2aclient.WithCallInterceptors(delegate.Interceptor(os.Getenv("LAGRANGE_TOKEN")))` : ''})
if err != nil {
	return err
}
res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
	Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(${quoted})),
})
`

  return [
    { id: 'cli', label: 'a2a CLI', code: `a2a -a ${url}${auth ? ` --auth "Bearer ${TOKEN}"` : ''} send ${shellQuote(text)}` },
    { id: 'curl', label: 'curl', code: curl.join(' \\\n') },
    { id: 'python', label: 'Python', code: python },
    { id: 'go', label: 'Go', code: go },
  ]
}

function shellQuote(s: string): string {
  return /^[\w@%+=:,./-]+$/.test(s) ? s : `'${s.replaceAll("'", "'\\''")}'`
}

export type Standing = { slug: string; name: string; role: string; status: string }

/** What a signed-in visitor can do with the address. */
export type Reach =
  | { kind: 'self' }
  | { kind: 'send'; via: string }
  | { kind: 'pending'; via: Standing }
  | { kind: 'ask'; from: Standing[] }
  | { kind: 'blocked' }
  | { kind: 'closed' }

/**
 * How the visitor reaches an address: through a workspace it's in or connected to (or
 * any, when public), or by asking for one of theirs. Asking takes the member role.
 */
export function reachOf(handle: string, me: string, access: string, workspaces: Standing[], current: string): Reach {
  if (handle === me) return { kind: 'self' }
  const through = workspaces.find((w) => w.status === 'in') ?? workspaces.find((w) => w.status === 'accepted')
  if (through) return { kind: 'send', via: through.slug }
  if (access === 'public') return { kind: 'send', via: current || workspaces[0]?.slug || '' }
  if (access !== 'connections') return { kind: 'closed' }
  const pending = workspaces.find((w) => w.status === 'pending')
  if (pending) return { kind: 'pending', via: pending }
  const from = workspaces.filter((w) => w.status !== 'blocked' && ['owner', 'admin', 'member'].includes(w.role))
  if (from.length > 0) return { kind: 'ask', from }
  return workspaces.some((w) => w.status === 'blocked') ? { kind: 'blocked' } : { kind: 'closed' }
}
