import { Pause, Play, Radio, SkipBack } from 'lucide-react'
import { useEffect } from 'react'
import { cn } from '@/lib/cn'
import { replayDelay, useLive, type Speed } from '@/store/live'

/** The scrubber: replays the activity log through the same reducer as the live view. */
export function ReplayBar() {
  const replay = useLive((s) => s.replay)!
  const { seek, step, setPlaying, setSpeed, closeReplay } = useLive.getState()
  const { entries, index, playing, speed } = replay

  useEffect(() => {
    if (!playing) return
    const t = setTimeout(() => step(), replayDelay(entries, index, speed))
    return () => clearTimeout(t)
  }, [playing, index, speed, entries, step])

  const current = entries[Math.max(0, index - 1)]
  return (
    <div className="absolute right-4 bottom-4 left-16 z-10 card flex items-center gap-3 px-3 py-2">
      <button className="rounded-lg p-1.5 text-muted hover:bg-surface-2 hover:text-ink" onClick={() => seek(0)} title="Back to the start">
        <SkipBack size={16} />
      </button>
      <button className="flex h-8 w-8 items-center justify-center rounded-full bg-accent text-accent-ink shadow-card" onClick={() => setPlaying(!playing)} title={playing ? 'Pause' : 'Play'}>
        {playing ? <Pause size={15} /> : <Play size={15} className="translate-x-px" />}
      </button>
      <div className="flex rounded-lg bg-surface-2 p-0.5">
        {([1, 4, 16] as Speed[]).map((s) => (
          <button key={s} onClick={() => setSpeed(s)} className={cn('rounded-md px-2 py-0.5 font-mono text-xs font-bold', s === speed ? 'bg-surface text-accent shadow-card' : 'text-muted')}>
            {s}×
          </button>
        ))}
      </div>
      <input
        type="range"
        min={0}
        max={entries.length}
        value={index}
        onChange={(e) => {
          setPlaying(false)
          seek(Number(e.target.value))
        }}
        className="min-w-0 flex-1 accent-[var(--accent)]"
        aria-label="Replay position"
      />
      <span className="w-40 truncate text-right font-mono text-[11px] text-muted">
        {current ? new Date(current.at).toLocaleTimeString() : '--:--'} · {index}/{entries.length}
      </span>
      <button className="flex items-center gap-1 rounded-lg px-2 py-1 text-xs font-bold text-good hover:bg-good-soft" onClick={closeReplay}>
        <Radio size={13} /> Live
      </button>
    </div>
  )
}
