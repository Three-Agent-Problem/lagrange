import { textOf, type Message } from '@/lib/api'
import { cn } from '@/lib/cn'
import { decisionOf, humanData } from '@/lib/data'
import { OUTSIDE } from '@/store/model'
import { Avatar } from '@/ui/Avatar'
import { DataView, Decision } from '@/ui/DataView'

/** One message of a task, as a chat bubble: a decision first, then the text, then its data as fields. */
export function Bubble({ m, sender, recipient, kind, mine }: { m: Message; sender: string; recipient: string; kind: (h: string) => 'agent' | 'human' | undefined; mine?: boolean }) {
  const fromSender = m.role === 'ROLE_USER'
  const who = fromSender ? sender || OUTSIDE : recipient
  const answeredBy = m.metadata?.['https://threeagents.dev/ext/answered-by/v1'] as string | undefined
  const isMine = mine ?? fromSender
  const data = humanData(m.parts)
  const decisions = data.map(decisionOf).filter((d) => d !== undefined)
  const rest = data.filter((d) => !decisionOf(d))
  const text = textOf(m.parts)
  return (
    <div className={cn('flex items-end gap-1.5', isMine && 'flex-row-reverse')}>
      <Avatar handle={answeredBy ?? who} kind={answeredBy ? 'human' : kind(who)} size={22} />
      <div className={cn('flex max-w-[85%] flex-col items-start gap-1 rounded-2xl px-3 py-1.5 text-[12.5px] shadow-card', isMine ? 'rounded-br-md bg-bubble-me' : 'rounded-bl-md bg-surface')}>
        {decisions.map((d, i) => (
          <Decision key={i} {...d} />
        ))}
        {text && <p className="whitespace-pre-wrap">{text}</p>}
        {rest.map((d, i) => (
          <DataView key={i} data={d} className="w-full" />
        ))}
        {answeredBy && <p className="text-[10px] font-semibold text-muted">answered by @{answeredBy}</p>}
      </div>
    </div>
  )
}
