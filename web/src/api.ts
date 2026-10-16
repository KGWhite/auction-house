import type { Auction, BidResponse, Filters, InboxItem, Me, User } from './types'

export class ApiError extends Error {
  status: number
  code: string
  minNextBid?: number

  constructor(status: number, code: string, message: string, minNextBid?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.minNextBid = minNextBid
  }
}

interface RequestOptions {
  method?: string
  userId?: number | null
  body?: unknown
  idempotencyKey?: string
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.userId) headers['X-Dev-User-Id'] = String(opts.userId)
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'

  let res: Response
  try {
    res = await fetch(path, {
      method: opts.method ?? 'GET',
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    })
  } catch {
    throw new ApiError(0, 'network_error', 'network error')
  }

  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = null
    }
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; min_next_bid?: number } } | null)?.error
    throw new ApiError(res.status, err?.code ?? 'unknown_error', err?.message ?? res.statusText, err?.min_next_bid)
  }
  return data as T
}

/** RFC 4122 v4 UUID; falls back when crypto.randomUUID is unavailable (non-secure contexts). */
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const b = crypto.getRandomValues(new Uint8Array(16))
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

export function auctionsPath(f: Filters): string {
  const p = new URLSearchParams()
  if (f.bracket !== 'all') p.set('bracket', String(f.bracket))
  if (f.type) p.set('type', f.type)
  p.set('sort', f.sort)
  return `/api/auctions?${p.toString()}`
}

export const api = {
  users: () => request<{ users: User[] }>('/api/users').then((r) => r.users),
  me: (userId: number) => request<Me>('/api/me', { userId }),
  inbox: (userId: number) => request<{ items: InboxItem[] }>('/api/me/inbox', { userId }).then((r) => r.items),
  auctions: (userId: number | null, f: Filters) =>
    request<{ auctions: Auction[] }>(auctionsPath(f), { userId }).then((r) => r.auctions),
  /** Every call gets a fresh Idempotency-Key. */
  placeBid: (userId: number, auctionId: number, amount: number) =>
    request<BidResponse>(`/api/auctions/${auctionId}/bids`, {
      method: 'POST',
      userId,
      body: { amount },
      idempotencyKey: newIdempotencyKey(),
    }),
}
