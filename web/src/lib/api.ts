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

export type User = { id: number; email: string; name: string; handle: string; admin: boolean }

export type Stats = { sessions: number; capacity: number; inFlight: number; waiting: number }

export type Session = { id: string; capacity: number; agent: string; expose: boolean; connected: string }

export type Address = {
  handle: string
  kind: 'agent' | 'human'
  ownerId?: number
  displayName: string
  description: string
  endpoint?: string
  card: AgentCard
  cardUrl: string
  online: boolean
  stats: Stats
  sessions: Session[]
  createdAt: string
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
  awaiting?: string
  createdAt: string
  updatedAt: string
  task?: A2ATask
  input?: { id?: string; schema: JSONSchema }
  challenge?: { id: string; integration: string; url: string; expiresAt: string }
}

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
  kind: 'task' | 'artifact' | 'message' | 'input' | 'queued' | 'delivered' | 'presence' | 'address'
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
  title: string
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

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204 || res.status === 202) return undefined as T
  const text = await res.text()
  const json = text ? JSON.parse(text) : undefined
  if (!res.ok) throw new ApiError(res.status, json?.error ?? res.statusText)
  return json as T
}

export const api = {
  me: () => call<{ user: User; publicUrl: string; dev?: boolean; devSeed?: boolean }>('GET', '/api/me'),
  /** Dev servers only (LAGRANGE_DEV=1): delete every agent and all traffic, keep people; with seed, run the seed again. */
  devReset: (seed: boolean) => call<void>('POST', '/api/dev/reset', { seed }),
  login: (email: string, password: string) => call<User>('POST', '/api/login', { email, password }),
  logout: () => call<void>('POST', '/api/logout', {}),
  users: () => call<{ users: User[] }>('GET', '/api/users'),
  invite: (u: { email: string; name: string; handle: string; password: string }) => call<User>('POST', '/api/users', u),
  addresses: () => call<{ addresses: Address[] }>('GET', '/api/addresses'),
  createAddress: (a: { handle: string; displayName?: string; description?: string; endpoint?: string }) =>
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
  runs: (f: { status?: RunStatus; q?: string; participant?: string; waitingOn?: string; archived?: '1' } = {}) => {
    const q = new URLSearchParams(Object.entries(f).filter(([, v]) => v) as [string, string][])
    return call<{ runs: Run[] }>('GET', `/api/runs${q.size ? `?${q}` : ''}`)
  },
  run: (id: string) => call<RunDetail>('GET', `/api/runs/${encodeURIComponent(id)}`),
  /** Hide a run from the list (true) or show it again (false). */
  waiting: () => call<{ waiting: Wait[]; policy: WaitPolicy }>('GET', '/api/waiting'),
  remind: (taskId: string) => call<void>('POST', `/api/waiting/${encodeURIComponent(taskId)}/remind`, {}),
  setWaitPolicy: (p: WaitPolicy) => call<{ policy: WaitPolicy }>('PUT', '/api/settings/wait-policy', p),
  archiveRun: (id: string, archived: boolean) => call<void>(archived ? 'POST' : 'DELETE', `/api/runs/${encodeURIComponent(id)}/archive`, {}),
}

/** The text parts of a message or artifact, joined. */
export function textOf(parts: Part[] | undefined): string {
  return (parts ?? [])
    .map((p) => p.text ?? '')
    .filter(Boolean)
    .join(' ')
}

/** The first data part, for rendering structured payloads. */
export function dataOf(parts: Part[] | undefined): unknown {
  return (parts ?? []).find((p) => p.data !== undefined)?.data
}
