// The live store: the server snapshot, kept current by the SSE stream, plus the replay of
// one run on the graph. Views read `view()`, which is the replayed model while a replay is open.
import { create } from 'zustand'
import { api, type Entry } from '@/lib/api'
import { emptyModel, FEED_MAX, fold, fromSnapshot, reduce, type Model } from '@/store/model'

export type Speed = 1 | 4 | 16

type Replay = {
  /** The run being replayed. */
  runId: string
  title: string
  entries: Entry[]
  /** How many entries are folded in. */
  index: number
  model: Model
  playing: boolean
  speed: Speed
}

type LiveState = {
  model: Model
  status: 'idle' | 'connecting' | 'live' | 'reconnecting'
  replay?: Replay
  start: () => () => void
  apply: (e: Entry) => void
  openReplay: (entries: Entry[], title: string, runId: string) => void
  closeReplay: () => void
  seek: (index: number) => void
  step: () => boolean
  setPlaying: (playing: boolean) => void
  setSpeed: (speed: Speed) => void
}

export const useLive = create<LiveState>((set, get) => ({
  model: emptyModel(),
  status: 'idle',

  start: () => {
    let source: EventSource | undefined
    let closed = false
    set({ status: 'connecting' })
    api
      .graph()
      .then((g) => {
        if (closed) return
        set({ model: fromSnapshot(g) })
        // The snapshot has state but no history: fetch the recent events for the feed.
        void api.activity(Math.max(0, g.seq - FEED_MAX), FEED_MAX).then(({ entries }) => {
          if (closed) return
          const known = entries.filter((e) => e.seq <= g.seq && e.kind !== 'presence')
          set((s) => ({ model: { ...s.model, feed: [...known, ...s.model.feed.filter((e) => e.seq > g.seq)].slice(-FEED_MAX) } }))
        })
        source = new EventSource(`/api/events?since=${g.seq}`)
        source.onopen = () => set({ status: 'live' })
        source.onerror = () => set({ status: 'reconnecting' })
        source.addEventListener('activity', (ev) => get().apply(JSON.parse((ev as MessageEvent<string>).data)))
      })
      .catch(() => set({ status: 'reconnecting' }))
    return () => {
      closed = true
      source?.close()
      set({ status: 'idle' })
    }
  },

  apply: (e) => set((s) => ({ model: reduce(s.model, e) })),

  openReplay: (entries, title, runId) => {
    const sorted = [...entries].sort((x, y) => x.seq - y.seq)
    set({ replay: { runId, title, entries: sorted, index: 0, model: emptyModel(), playing: sorted.length > 0, speed: 4 } })
  },

  closeReplay: () => set({ replay: undefined }),

  seek: (index) => {
    const r = get().replay
    if (!r) return
    const i = Math.max(0, Math.min(r.entries.length, index))
    // Forward: fold the gap. Backward: refold from the start (the reducer is cheap).
    const model = i >= r.index ? fold(r.entries.slice(r.index, i), r.model) : fold(r.entries.slice(0, i))
    set({ replay: { ...r, index: i, model } })
  },

  step: () => {
    const r = get().replay
    if (!r || r.index >= r.entries.length) {
      if (r) set({ replay: { ...r, playing: false } })
      return false
    }
    get().seek(r.index + 1)
    return true
  },

  setPlaying: (playing) => {
    const r = get().replay
    if (!r) return
    // Playing from the end starts over.
    if (playing && r.index >= r.entries.length) {
      set({ replay: { ...r, index: 0, model: emptyModel(), playing } })
      return
    }
    set({ replay: { ...r, playing } })
  },

  setSpeed: (speed) => {
    const r = get().replay
    if (r) set({ replay: { ...r, speed } })
  },
}))

/** The model views draw: the replay while one is open, otherwise live. */
export const useView = () => useLive((s) => s.replay?.model ?? s.model)

/** Real time to wait before the next replay entry, compressed by speed; long gaps are cut. */
export function replayDelay(entries: Entry[], index: number, speed: Speed): number {
  if (index <= 0 || index >= entries.length) return 250 / speed
  const gap = Date.parse(entries[index].at) - Date.parse(entries[index - 1].at)
  return Math.max(16, Math.min(1500, gap) / speed)
}
