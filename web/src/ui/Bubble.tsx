import { dataOf, textOf, type Message } from '@/lib/api'
import { cn } from '@/lib/cn'
import { OUTSIDE } from '@/store/model'
import { Avatar } from '@/ui/Avatar'

/** One message of a task, as a chat bubble. */
export function Bubble({ m, sender, recipient, kind, mine }: { m: Message; sender: string; recipient: string; kind: (h: string) => 'agent' | 'human' | undefined; mine?: boolean }) {
  const fromSender = m.role === 'ROLE_USER'
  const who = fromSender ? sender || OUTSIDE : recipient
  const answeredBy = m.metadata?.['https://threeagents.dev/ext/answered-by/v1'] as string | undefined
  const isMine = mine ?? fromSender
  const data = dataOf(m.parts)
  return (
    <div className={cn('flex items-end gap-1.5', isMine && 'flex-row-reverse')}>
      <Avatar handle={answeredBy ?? who} kind={answeredBy ? 'human' : kind(who)} size={22} />
      <div className={cn('max-w-[85%] rounded-2xl px-3 py-1.5 text-[12.5px] shadow-card', isMine ? 'rounded-br-md bg-bubble-me' : 'rounded-bl-md bg-surface')}>
        {textOf(m.parts) && <p className="whitespace-pre-wrap">{textOf(m.parts)}</p>}
        {data !== undefined && <pre className="scrollbar-thin mt-1 max-h-40 overflow-auto rounded-lg bg-surface-2 p-1.5 font-mono text-[10px]">{JSON.stringify(data, null, 2)}</pre>}
        {answeredBy && <p className="mt-0.5 text-[10px] font-semibold text-muted">answered by @{answeredBy}</p>}
      </div>
    </div>
  )
}
