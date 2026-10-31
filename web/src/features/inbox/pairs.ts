// A conversation between two other addresses, as one timeline: the tasks one sent the
// other and their plain message exchanges. Pure, so it is tested without a browser.
import type { Conversation, Entry, Message, Part, TaskView } from '@/lib/api'

export type Item = { kind: 'task'; at: number; task: TaskView } | { kind: 'message'; at: number; ask: Message; reply?: Message; sender: string; recipient: string }

/** Tasks and message exchanges, oldest first. */
export function timeline(tasks: TaskView[], messages: Entry[]): Item[] {
  const items: Item[] = tasks.map((t) => ({ kind: 'task' as const, at: Date.parse(t.createdAt), task: t }))
  for (const e of messages) {
    const ask = typeof e.data?.request === 'string' ? e.data.request : (e.summary ?? '')
    const reply = typeof e.data?.replyContent === 'string' ? e.data.replyContent : typeof e.data?.reply === 'string' ? e.data.reply : ''
    items.push({
      kind: 'message',
      at: Date.parse(e.at),
      sender: e.sender ?? '',
      recipient: e.recipient ?? '',
      ask: { messageId: `${e.seq}-ask`, role: 'ROLE_USER', parts: partsOf(ask) },
      reply: reply ? { messageId: `${e.seq}-reply`, role: 'ROLE_AGENT', parts: partsOf(reply) } : undefined,
    })
  }
  return items.sort((a, b) => a.at - b.at)
}

/** A recorded preview back into parts: JSON object lines become data, the rest text. */
export function partsOf(preview: string): Part[] {
  const parts: Part[] = []
  const text: string[] = []
  for (const line of preview.split('\n')) {
    const t = line.trim()
    if (t.startsWith('{') && t.endsWith('}')) {
      try {
        parts.push({ data: JSON.parse(t) })
        continue
      } catch {
        /* not JSON after all */
      }
    }
    text.push(line)
  }
  const joined = text.join('\n').trim()
  return joined ? [{ text: joined }, ...parts] : parts
}

/** Conversations that don't involve me: what the other addresses said to each other. */
export function others(cs: Conversation[], me: string, q: string): Conversation[] {
  const needle = q.trim().toLowerCase().replace(/^@/, '')
  return cs.filter((c) => c.a !== me && c.b !== me && (!needle || c.a.includes(needle) || c.b.includes(needle)))
}
