import { create } from 'zustand'
import { api, ApiError, type Address, type User } from '@/lib/api'

type SessionState = {
  user?: User
  publicUrl: string
  /** The server runs with LAGRANGE_DEV=1. */
  dev: boolean
  /** The dev server has a seed command (LAGRANGE_DEV_SEED). */
  devSeed: boolean
  /** undefined until the first /api/me answer. */
  ready: boolean
  addresses: Address[]
  load: () => Promise<void>
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refreshAddresses: () => Promise<void>
}

export const useSession = create<SessionState>((set, get) => ({
  publicUrl: location.origin,
  dev: false,
  devSeed: false,
  ready: false,
  addresses: [],
  load: async () => {
    try {
      const me = await api.me()
      set({ user: me.user, publicUrl: me.publicUrl, dev: !!me.dev, devSeed: !!me.devSeed, ready: true })
      await get().refreshAddresses()
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) set({ user: undefined, ready: true })
      else throw e
    }
  },
  login: async (email, password) => {
    await api.login(email, password)
    await get().load()
  },
  logout: async () => {
    await api.logout()
    set({ user: undefined, addresses: [] })
  },
  refreshAddresses: async () => {
    const { addresses } = await api.addresses()
    set({ addresses })
  },
}))

export const addressMap = (addresses: Address[]) => Object.fromEntries(addresses.map((a) => [a.handle, a])) as Record<string, Address>
