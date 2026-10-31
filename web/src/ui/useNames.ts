import { useCallback, useMemo } from 'react'
import { OUTSIDE } from '@/store/model'
import { addressMap, useSession } from '@/store/session'

/** How to show an address: a person's name, else @handle; and its kind. */
export function useNames() {
  const addresses = useSession((s) => s.addresses)
  const byHandle = useMemo(() => addressMap(addresses), [addresses])
  const kind = useCallback((h: string) => byHandle[h]?.kind, [byHandle])
  const name = useCallback(
    (h: string) => {
      if (!h || h === OUTSIDE) return 'Outside client'
      const a = byHandle[h]
      return a?.kind === 'human' ? a.displayName || h : `@${h}`
    },
    [byHandle],
  )
  return { kind, name }
}
