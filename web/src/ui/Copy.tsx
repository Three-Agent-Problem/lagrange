import { Check, Copy as CopyIcon } from 'lucide-react'
import { useState } from 'react'
import { cn } from '@/lib/cn'

export function Copy({ text, className, label }: { text: string; className?: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className={cn('inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs font-semibold text-muted hover:bg-surface-2 hover:text-ink', className)}
      onClick={() => {
        void navigator.clipboard?.writeText(text)
        setDone(true)
        setTimeout(() => setDone(false), 1200)
      }}
      title="Copy"
    >
      {done ? <Check size={13} className="text-good" /> : <CopyIcon size={13} />}
      {label}
    </button>
  )
}

export function CodeLine({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg bg-ink/95 px-3 py-2 font-mono text-[12px] text-white dark:bg-black/60">
      <code className="min-w-0 flex-1 break-all">{text}</code>
      <Copy text={text} className="text-white/70 hover:bg-white/10 hover:text-white" />
    </div>
  )
}
