// The live store: the server snapshot, kept current by the SSE stream, plus the replay
// scrubber. Views read `view()`, which is the replayed model while a replay is open.
import { create } from 'zustand'
import { api, type Entry } from '@/lib/api'
import { emptyModel, fold, fromSnapshot, reduce, type Model } from '@/store/model'

export type Speed = 1 | 4 | 16

type Replay = {
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
  openReplay: () => Promise<void>
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

  openReplay: async () => {
    const entries: Entry[] = []
    for (let since = 0; ; ) {
      const page = (await api.activity(since, 5000)).entries
      entries.push(...page)
      if (page.length < 5000) break
      since = page[page.length - 1].seq
    }
    set({ replay: { entries, index: 0, model: emptyModel(), playing: entries.length > 0, speed: 4 } })
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
