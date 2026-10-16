export function formatGold(n: number): string {
  return n.toLocaleString('zh-TW')
}

export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString('zh-TW', { hour12: false })
}

/** Whole seconds elapsed since `since` (ms epoch), never negative. */
export function secondsAgo(since: number, now: number): number {
  return Math.max(0, Math.floor((now - since) / 1000))
}

/** Parses a bid input; returns null unless it is a positive integer. */
export function parseAmount(raw: string): number | null {
  const s = raw.trim().replace(/,/g, '')
  if (!/^\d+$/.test(s)) return null
  const n = Number(s)
  return Number.isSafeInteger(n) && n > 0 ? n : null
}
