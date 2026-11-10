// Types and calls for the Lagrange API (platform/api.go). A2A objects use the A2A 1.0
// JSON shapes (lf.a2a.v1, camelCase).

export type Part = {
  text?: string
  data?: unknown
  url?: string
  raw?: string
  mediaType?: string
  filename?: string
  metadata?: Record<string, unknown>
}

export type Message = {
  messageId: string
  role: 'ROLE_USER' | 'ROLE_AGENT'
  parts: Part[]
  taskId?: string
  contextId?: string
  metadata?: Record<string, unknown>
}

export type Artifact = { artifactId: string; name?: string; description?: string; parts: Part[] }

export type A2ATask = {
  id: string
  contextId: string
  status: { state: string; message?: Message; timestamp?: string }
  artifacts?: Artifact[]
  history?: Message[]
  metadata?: Record<string, unknown>
}

export type Skill = { id: string; name: string; description: string; tags?: string[]; examples?: string[] }

export type AgentCard = {
  name: string
  description: string
  version: string
  supportedInterfaces: { url: string; protocolBinding: string }[]
  capabilities: { streaming?: boolean; pushNotifications?: boolean; extensions?: { uri: string }[] }
  skills: Skill[]
  provider?: { organization: string; url: string }
}

export type User = { id: number; email: string; name: string; handle: string; admin: boolean; verified: boolean; hasPassword: boolean }

/** How people can sign in on this server. */
export type AuthMethods = { signup: 'open' | 'invite'; email: boolean; providers: { name: string; label: string }[] }

export type Identity = { provider: string; email: string; createdAt: string }

export type Role = 'owner' | 'admin' | 'member' | 'viewer'

/** A team: its agents and its members. role is yours in it. */
export type Workspace = { id: number; slug: string; name: string; role: Role }

export type Member = User & { role: Role; joinedAt: string }

/** A workspace's webhook endpoint, without its secret. delivered/failed count the last day. */
export type Webhook = { id: number; url: string; description: string; events: string[]; active: boolean; createdAt: string; delivered: number; failed: number; pending: number }

export type WebhookDelivery = {
  id: number
  eventId: string
  type: string
  status: 'pending' | 'delivered' | 'failed'
  attempts: number
  lastStatus?: number
  lastError?: string
  nextAt?: string
  createdAt: string
  deliveredAt?: string
  payload: unknown
}

/** A personal API key, without its secret. */
export type APIKey = { id: number; name: string; prefix: string; role: Role; workspace: string; createdAt: string; lastUsed?: string }

/** An invitation into a workspace. existing: the email has an account, so they log in to join. */
export type Invite = { email: string; invitedBy: string; workspace: string; role: Role; existing: boolean; link?: string; emailed: boolean }

export type Me = { user: User; identities: Identity[]; workspaces: Workspace[]; auth: AuthMethods; publicUrl: string; dev?: boolean; devSeed?: boolean }

export type Stats = { sessions: number; capacity: number; inFlight: number; waiting: number }

export type Session = { id: string; capacity: number; agent: string; expose: boolean; connected: string }

export type Address = {
  handle: string
  kind: 'agent' | 'human'
  ownerId?: number
  displayName: string
  description: string
  endpoint?: string
  /** Who may start a task with it. */
  access: Access
  card: AgentCard
  cardUrl: string
  online: boolean
  /** In another workspace: shown because it talked to this one. */
  external?: boolean
  stats: Stats
  sessions: Session[]
  createdAt: string
}

export type Access = 'public' | 'workspace' | 'connections'

export type ConnectionStatus = 'pending' | 'accepted' | 'declined' | 'blocked'

/** A workspace's standing to reach an address of another workspace. */
export type Connection = {
  id: number
  from: number
  fromName: string
  to: string
  status: ConnectionStatus
  note: string
  ratePerMin: number
  requestedBy?: string
  decidedBy?: string
  createdAt: string
  decidedAt?: string
}

/** An address's public page: its card, how to reach it, and the visitor's standing. */
export type Profile = {
  handle: string
  kind: 'agent' | 'human'
  name: string
  description: string
  url: string
  cardUrl: string
  card: AgentCard
  access: Access
  online: boolean
  workspace?: string
  check: { grade: 'conformant' | 'near-conformant' | 'non-conformant'; findings: { severity: 'error' | 'warning'; path: string; message: string }[] | null }
  viewer?: { handle: string; workspaces: { slug: string; name: string; role: Role; status: string }[] }
}

export type Token = { handle: string; label: string; prefix: string; createdAt: string; lastUsed?: string }

export type TaskView = {
  id: string
  contextId: string
  state: string
  sender: string
  recipient: string
  parentTaskId?: string
  summary: string
  /** What the latest status (or the final result) says, in words. */
  status?: string
  awaiting?: string
  createdAt: string
  updatedAt: string
  task?: A2ATask
  input?: { id?: string; schema: JSONSchema }
  challenge?: { id: string; integration: string; url: string; expiresAt: string }
}

/** What two addresses said to each other (a and b sorted). */
export type Conversation = { a: string; b: string; tasks: number; messages: number; waiting: number; lastAt: string; last: string; lastFrom: string }

/** The team's rules for tasks waiting on someone. Delays are seconds; null is off. */
export type WaitPolicy = { remindAfter: number | null; escalateAfter: number | null; escalateTo: string; cancelAfter: number | null }

/** A task waiting on someone, on the waiting board. */
export type Wait = {
  task: TaskView
  runId: string
  who: string
  asker: string
  person: boolean
  since: string
  reminders: number
  remindedAt?: string
  escalatedTo?: string
  escalatedAt?: string
  remindAt?: string
  escalateAt?: string
  cancelAt?: string
}

export type JSONSchema = {
  type?: string | string[]
  title?: string
  description?: string
  properties?: Record<string, JSONSchema>
  required?: string[]
  enum?: unknown[]
  default?: unknown
}

/** One row of the activity log (activity.Entry). */
export type Entry = {
  seq: number
  at: string
  kind: 'task' | 'artifact' | 'message' | 'input' | 'queued' | 'delivered' | 'presence' | 'address' | 'usage'
  taskId?: string
  parentTaskId?: string
  contextId?: string
  sender?: string
  recipient?: string
  state?: string
  summary?: string
  data?: Record<string, unknown>
}

export type Link = { taskId: string; sender: string; recipient: string; parentTaskId?: string }

/** One HTTP request and its response, as the platform carried it (activity.Exchange). */
export type Exchange = {
  id: number
  at: string
  /** in: a call to an address; out: the platform's call to the agent; push: a webhook. */
  direction: 'in' | 'out' | 'push'
  taskId?: string
  parentTaskId?: string
  contextId?: string
  sender?: string
  recipient?: string
  method: string
  url: string
  status: number
  durationMs: number
  headers?: { request?: Record<string, string>; response?: Record<string, string> }
  request: string
  response: string
  truncated?: boolean
  error?: string
}

/** One major action: a task nobody delegated, with everything delegated from it. */
export type Run = {
  /** The root task's ID, or msg-<seq> for a plain message exchange. */
  id: string
  /** The root task's A2A context. */
  contextId?: string
  title: string
  /** How it ended, in words; empty while it runs. */
  outcome?: string
  starter: string
  recipient: string
  /** The root task's state, or "message". */
  state: string
  waiting: string[]
  participants: string[]
  tasks: number
  entries: number
  startedAt: string
  updatedAt: string
  /** When the longest-waiting task in the run started waiting. */
  waitingSince?: string
  /** Hidden from the runs list. */
  archived?: boolean
  /** Tasks of the run inside other workspaces, which this one doesn't see. */
  hidden?: number
  /** What the run's agents reported spending on it, in `currency`. */
  cost?: number
  currency?: string
}

export type Answer = { text?: string; data?: Record<string, unknown>; decision?: 'reject' }

/** A task as a decision link shows it (GET /api/decisions/{token}). */
export type Decision = { task: TaskView; for: { handle: string; name: string }; expiresAt: string }

export type NotificationSettings = { email: boolean; slackWebhook: string }

export type NotificationSettingsView = { settings: NotificationSettings; emailAvailable: boolean; email: string }

export type RunStatus = 'running' | 'waiting' | 'done' | 'failed'

export type RunDetail = { run: Run; tasks: TaskView[]; entries: Entry[]; exchanges: Exchange[] }

export type Graph = { seq: number; addresses: Address[]; tasks: TaskView[] }

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

let workspace = ''

/** Every call from now on acts in workspace slug (the Lagrange-Workspace header). */
export function actIn(slug: string) {
  workspace = slug
}

/** The event stream URL; EventSource can't send headers, so the workspace goes in the query. */
export function eventsURL(since: number): string {
  return `/api/events?since=${since}${workspace ? `&workspace=${encodeURIComponent(workspace)}` : ''}`
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (workspace) headers['Lagrange-Workspace'] = workspace
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204 || res.status === 202) return undefined as T
  const text = await res.text()
  const json = text ? JSON.parse(text) : undefined
  if (!res.ok) throw new ApiError(res.status, json?.error ?? res.statusText)
  return json as T
}

export const api = {
  me: () => call<Me>('GET', '/api/me'),
  authMethods: () => call<AuthMethods>('GET', '/api/auth/methods'),
  signup: (u: { email: string; name: string; handle: string; password: string }) => call<User>('POST', '/api/signup', u),
  verify: (token: string) => call<User>('POST', '/api/auth/verify', { token }),
  resendVerify: () => call<void>('POST', '/api/auth/verify/resend', {}),
  /** Emails a login link; answers the same whether or not the email has an account. */
  loginLink: (email: string) => call<void>('POST', '/api/login/link', { email }),
  claimLoginLink: (token: string) => call<User>('POST', '/api/login/link/claim', { token }),
  resetPassword: (email: string) => call<void>('POST', '/api/password/reset', { email }),
  confirmReset: (token: string, password: string) => call<User>('POST', '/api/password/reset/confirm', { token, password }),
  changePassword: (current: string, password: string) => call<void>('PUT', '/api/me/password', { current, password }),
  createInvite: (email: string, role: Role) => call<Invite>('POST', '/api/invites', { email, role }),
  invite: (token: string) => call<Invite>('GET', `/api/invites/${encodeURIComponent(token)}`),
  /** Without u, joins with the account you are logged in as (the invited email's). */
  acceptInvite: (token: string, u: { name: string; handle: string; password: string } | Record<string, never> = {}) =>
    call<User>('POST', `/api/invites/${encodeURIComponent(token)}/accept`, u),
  workspaces: () => call<{ workspaces: Workspace[] }>('GET', '/api/workspaces'),
  createWorkspace: (name: string) => call<{ workspace: Workspace }>('POST', '/api/workspaces', { name }),
  renameWorkspace: (name: string) => call<{ workspace: Workspace }>('PUT', '/api/workspace', { name }),
  members: () => call<{ members: Member[]; workspace: Workspace }>('GET', '/api/workspace/members'),
  setRole: (userId: number, role: Role) => call<void>('PUT', `/api/workspace/members/${userId}`, { role }),
  removeMember: (userId: number) => call<void>('DELETE', `/api/workspace/members/${userId}`, {}),
  profile: (handle: string) => call<Profile>('GET', `/api/public/addresses/${encodeURIComponent(handle)}`),
  setAccess: (handle: string, access: Access) => call<void>('PUT', `/api/addresses/${encodeURIComponent(handle)}/access`, { access }),
  connections: () => call<{ incoming: Connection[]; outgoing: Connection[] }>('GET', '/api/connections'),
  requestConnection: (to: string, note: string) => call<{ connection: Connection }>('POST', '/api/connections', { to, note }),
  decideConnection: (id: number, status: ConnectionStatus, ratePerMin = 0) => call<{ connection: Connection }>('PUT', `/api/connections/${id}`, { status, ratePerMin }),
  endConnection: (id: number) => call<void>('DELETE', `/api/connections/${id}`, {}),
  /** Dev servers only (LAGRANGE_DEV=1): delete every agent and all traffic, keep people; with seed, run the seed again. */
  devReset: (seed: boolean) => call<void>('POST', '/api/dev/reset', { seed }),
  login: (email: string, password: string) => call<User>('POST', '/api/login', { email, password }),
  logout: () => call<void>('POST', '/api/logout', {}),
  users: () => call<{ users: User[] }>('GET', '/api/users'),
  addresses: () => call<{ addresses: Address[] }>('GET', '/api/addresses'),
  createAddress: (a: { handle: string; displayName?: string; description?: string; endpoint?: string; access?: Access }) =>
    call<{ address: Address; token: string; command: string }>('POST', '/api/addresses', a),
  deleteAddress: (handle: string) => call<void>('DELETE', `/api/addresses/${handle}`, {}),
  tokens: (handle: string) => call<{ tokens: Token[] }>('GET', `/api/addresses/${handle}/tokens`),
  mintToken: (handle: string, label: string) => call<{ token: string; command: string }>('POST', `/api/addresses/${handle}/tokens`, { label }),
  inbox: () => call<{ tasks: TaskView[] }>('GET', '/api/inbox'),
  tasks: (mine = false) => call<{ tasks: TaskView[] }>('GET', `/api/tasks${mine ? '?mine=1' : ''}`),
  task: (id: string) => call<{ task: TaskView; activity: Entry[]; children: Link[] }>('GET', `/api/tasks/${id}`),
  answer: (id: string, a: Answer) => call<void>('POST', `/api/tasks/${id}/answer`, a),
  decision: (token: string) => call<Decision>('GET', `/api/decisions/${encodeURIComponent(token)}`),
  answerDecision: (token: string, a: Answer) => call<void>('POST', `/api/decisions/${encodeURIComponent(token)}`, a),
  notificationSettings: () => call<NotificationSettingsView>('GET', '/api/settings/notifications'),
  setNotificationSettings: (s: NotificationSettings) => call<NotificationSettingsView>('PUT', '/api/settings/notifications', s),
  testNotification: () => call<void>('POST', '/api/settings/notifications/test', {}),
  cancel: (id: string) => call<{ task: A2ATask }>('POST', `/api/tasks/${id}/cancel`, {}),
  send: (m: { to: string; text?: string; data?: Record<string, unknown>; taskId?: string; contextId?: string }) =>
    call<{ result: A2ATask | Message }>('POST', '/api/send', m),
  graph: () => call<Graph>('GET', '/api/graph'),
  activity: (since: number, limit = 2000) => call<{ entries: Entry[] }>('GET', `/api/activity?since=${since}&limit=${limit}`),
  runs: (f: { status?: RunStatus; q?: string; participant?: string; waitingOn?: string; archived?: '1'; limit?: string } = {}) => {
    const q = new URLSearchParams(Object.entries(f).filter(([, v]) => v) as [string, string][])
    return call<{ runs: Run[] }>('GET', `/api/runs${q.size ? `?${q}` : ''}`)
  },
  run: (id: string) => call<RunDetail>('GET', `/api/runs/${encodeURIComponent(id)}`),
  /** Hide a run from the list (true) or show it again (false). */
  conversations: () => call<{ conversations: Conversation[] }>('GET', '/api/conversations'),
  conversation: (a: string, b: string) => call<{ tasks: TaskView[]; messages: Entry[] }>('GET', `/api/conversations/${encodeURIComponent(a)}/${encodeURIComponent(b)}`),
  waiting: () => call<{ waiting: Wait[]; policy: WaitPolicy }>('GET', '/api/waiting'),
  remind: (taskId: string) => call<void>('POST', `/api/waiting/${encodeURIComponent(taskId)}/remind`, {}),
  setWaitPolicy: (p: WaitPolicy) => call<{ policy: WaitPolicy }>('PUT', '/api/settings/wait-policy', p),
  webhooks: () => call<{ webhooks: Webhook[]; events: string[] }>('GET', '/api/webhooks'),
  createWebhook: (h: { url: string; description: string; events: string[] }) => call<{ webhook: Webhook; secret: string }>('POST', '/api/webhooks', h),
  updateWebhook: (id: number, h: { active?: boolean; events?: string[]; url?: string; description?: string }) => call<void>('PUT', `/api/webhooks/${id}`, h),
  deleteWebhook: (id: number) => call<void>('DELETE', `/api/webhooks/${id}`, {}),
  webhookDeliveries: (id: number) => call<{ deliveries: WebhookDelivery[] }>('GET', `/api/webhooks/${id}/deliveries`),
  testWebhook: (id: number) => call<void>('POST', `/api/webhooks/${id}/test`, {}),
  retryDelivery: (id: number, delivery: number) => call<void>('POST', `/api/webhooks/${id}/deliveries/${delivery}/retry`, {}),
  apiKeys: () => call<{ keys: APIKey[] }>('GET', '/api/keys'),
  createAPIKey: (k: { name: string; workspace: string; role: Role }) => call<{ key: APIKey; secret: string }>('POST', '/api/keys', k),
  revokeAPIKey: (id: number) => call<void>('DELETE', `/api/keys/${id}`, {}),
  /** Approves a `lagrange login`; redirect is set when the CLI listens on 127.0.0.1. */
  authorizeCLI: (r: { challenge: string; name: string; port: number; workspace: string; role: Role }) =>
    call<{ code: string; redirect?: string }>('POST', '/api/cli/authorize', r),
  archiveRun: (id: string, archived: boolean) => call<void>(archived ? 'POST' : 'DELETE', `/api/runs/${encodeURIComponent(id)}/archive`, {}),
}

/** The text parts of a message or artifact, joined as they are: a streamed answer arrives as one part per chunk. */
export function textOf(parts: Part[] | undefined): string {
  return (parts ?? [])
    .map((p) => p.text ?? '')
    .filter(Boolean)
    .join('')
}

/** The first data part, for rendering structured payloads. */
export function dataOf(parts: Part[] | undefined): unknown {
  return (parts ?? []).find((p) => p.data !== undefined)?.data
}
