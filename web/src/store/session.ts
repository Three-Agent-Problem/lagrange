import { create } from 'zustand'
import { atLeast, pickWorkspace } from '@/features/workspaces/roles'
import { actIn, api, ApiError, type Address, type AuthMethods, type Identity, type Role, type User, type Workspace } from '@/lib/api'

type SessionState = {
  user?: User
  identities: Identity[]
  workspaces: Workspace[]
  /** The workspace the app acts in; undefined when you are in none. */
  workspace?: Workspace
  auth?: AuthMethods
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
  /** Acts in another workspace from now on (it reloads the app). */
  switchWorkspace: (slug: string) => void
}

const WORKSPACE_KEY = 'lagrange-workspace'

function savedWorkspace(): string {
  try {
    return localStorage.getItem(WORKSPACE_KEY) ?? ''
  } catch {
    return ''
  }
}

/** Whether you have at least role min in the current workspace. */
export const useCan = (min: Role) => useSession((s) => atLeast(s.workspace?.role, min))

export const useSession = create<SessionState>((set, get) => ({
  publicUrl: location.origin,
  dev: false,
  devSeed: false,
  ready: false,
  addresses: [],
  identities: [],
  workspaces: [],
  load: async () => {
    try {
      const me = await api.me()
      const workspace = pickWorkspace(me.workspaces ?? [], savedWorkspace())
      actIn(workspace?.slug ?? '')
      set({ user: me.user, identities: me.identities, workspaces: me.workspaces ?? [], workspace, auth: me.auth, publicUrl: me.publicUrl, dev: !!me.dev, devSeed: !!me.devSeed, ready: true })
      // Until the email is confirmed, the rest of the API says no.
      if (me.user.verified && workspace) await get().refreshAddresses()
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
  switchWorkspace: (slug) => {
    try {
      localStorage.setItem(WORKSPACE_KEY, slug)
    } catch {
      /* storage unavailable: the oldest workspace it is */
    }
    // Every view and the live stream start over in the new workspace.
    location.assign('/')
  },
}))

export const addressMap = (addresses: Address[]) => Object.fromEntries(addresses.map((a) => [a.handle, a])) as Record<string, Address>
