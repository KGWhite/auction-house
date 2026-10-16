import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from './api'
import { AuctionTable } from './components/AuctionTable'
import { FilterBar } from './components/FilterBar'
import { InboxPanel } from './components/InboxPanel'
import { StatusBar } from './components/StatusBar'
import { secondsAgo } from './format'
import type { Filters } from './types'

const POLL_MS = 2000

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

export default function App() {
  const [userId, setUserId] = useState<number | null>(null)
  const [filters, setFilters] = useState<Filters>({ bracket: 'all', type: '', sort: 'ending' })

  const usersQuery = useQuery({ queryKey: ['users'], queryFn: api.users, staleTime: Infinity })
  const users = usersQuery.data ?? []

  const selectUser = (id: number) => {
    setUserId(id)
    const u = users.find((x) => x.id === id)
    // The bracket filter defaults to the selected player's own bracket.
    if (u) setFilters((f) => ({ ...f, bracket: u.level_bracket }))
  }

  // Pick the first player once the list arrives.
  useEffect(() => {
    if (userId === null && users.length > 0) {
      setUserId(users[0].id)
      setFilters((f) => ({ ...f, bracket: users[0].level_bracket }))
    }
  }, [userId, users])

  const meQuery = useQuery({
    queryKey: ['me', userId],
    queryFn: () => api.me(userId!),
    enabled: userId !== null,
    refetchInterval: POLL_MS,
  })

  const auctionsQuery = useQuery({
    queryKey: ['auctions', userId, filters],
    queryFn: () => api.auctions(userId, filters),
    enabled: userId !== null,
    refetchInterval: POLL_MS,
    placeholderData: keepPreviousData,
  })

  const inboxQuery = useQuery({
    queryKey: ['inbox', userId],
    queryFn: () => api.inbox(userId!),
    enabled: userId !== null,
    refetchInterval: POLL_MS,
  })

  const now = useNow(1000)
  const updatedAgo = auctionsQuery.dataUpdatedAt > 0 ? secondsAgo(auctionsQuery.dataUpdatedAt, now) : null
  const failed = usersQuery.isError || meQuery.isError || auctionsQuery.isError

  return (
    <div className="min-h-screen">
      <StatusBar users={users} userId={userId} me={meQuery.data} onSelectUser={selectUser} />
      <main className="mx-auto max-w-6xl space-y-4 px-4 py-4">
        {failed && (
          <div role="alert" className="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 ring-1 ring-red-200">
            無法連線到伺服器，請確認 API 已啟動（make server）。系統會自動重試。
          </div>
        )}
        <FilterBar filters={filters} secondsSinceUpdate={updatedAgo} onChange={setFilters} />
        <AuctionTable auctions={auctionsQuery.data ?? []} me={meQuery.data} userId={userId} />
        <InboxPanel items={inboxQuery.data ?? []} />
      </main>
    </div>
  )
}
