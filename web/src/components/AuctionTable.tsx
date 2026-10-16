import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../api'
import { formatGold, parseAmount } from '../format'
import { TIME_BAND_LABELS, TIME_BAND_STYLES, bracketLabel, errorMessage, itemTypeLabel } from '../labels'
import type { Auction, Me } from '../types'

interface TableProps {
  auctions: Auction[]
  me: Me | undefined
  userId: number | null
}

export function AuctionTable({ auctions, me, userId }: TableProps) {
  return (
    <div className="overflow-x-auto rounded-lg bg-white shadow-sm ring-1 ring-slate-200">
      <table className="w-full min-w-[56rem] text-left text-sm">
        <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500">
          <tr>
            <th className="px-4 py-3 font-medium">物品</th>
            <th className="px-4 py-3 font-medium">目前價格 / 最低下一口價</th>
            <th className="px-4 py-3 font-medium">最高出價者</th>
            <th className="px-4 py-3 font-medium">剩餘時間</th>
            <th className="px-4 py-3 font-medium">出價</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {auctions.length === 0 ? (
            <tr>
              <td colSpan={5} className="px-4 py-10 text-center text-slate-400">
                目前沒有符合條件的拍賣
              </td>
            </tr>
          ) : (
            auctions.map((a) => <AuctionRow key={a.id} auction={a} me={me} userId={userId} />)
          )}
        </tbody>
      </table>
    </div>
  )
}

function AuctionRow({ auction: a, me, userId }: { auction: Auction; me: Me | undefined; userId: number | null }) {
  return (
    <tr className="align-top hover:bg-slate-50/60" data-testid={`auction-${a.id}`}>
      <td className="px-4 py-3">
        <div className="font-medium">{a.item.name}</div>
        <div className="text-xs text-slate-500">
          {itemTypeLabel(a.item.type)} · {a.item.level} 級
        </div>
      </td>
      <td className="px-4 py-3 tabular-nums">
        {a.has_bids ? (
          <>
            <div className="font-semibold">{formatGold(a.current_price)}</div>
            <div className="text-xs text-slate-500">最低下一口價 {formatGold(a.min_next_bid)}</div>
          </>
        ) : (
          <>
            <div className="font-semibold">
              <span className="mr-1 text-xs font-normal text-slate-500">起標價</span>
              {formatGold(a.current_price)}
            </div>
          </>
        )}
      </td>
      <td className="px-4 py-3">
        <TopBidder auction={a} />
      </td>
      <td className="px-4 py-3">
        <span className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ${TIME_BAND_STYLES[a.time_band]}`}>
          {TIME_BAND_LABELS[a.time_band]}
        </span>
      </td>
      <td className="px-4 py-3">
        <BidCell auction={a} me={me} userId={userId} />
      </td>
    </tr>
  )
}

function TopBidder({ auction: a }: { auction: Auction }) {
  if (a.is_mine) return <span className="font-semibold text-emerald-600">你是最高者</span>
  if (a.i_have_bid) {
    return (
      <div>
        <span className="font-semibold text-yellow-600">你已被超越</span>
        {a.top_bidder_name && <div className="text-xs text-slate-500">最高：{a.top_bidder_name}</div>}
      </div>
    )
  }
  if (!a.has_bids) return <span className="text-slate-400">尚無出價</span>
  return <span>{a.top_bidder_name}</span>
}

type Feedback = { kind: 'ok' | 'error'; text: string } | null

function BidCell({ auction: a, me, userId }: { auction: Auction; me: Me | undefined; userId: number | null }) {
  const queryClient = useQueryClient()
  const [value, setValue] = useState(String(a.min_next_bid))
  const [dirty, setDirty] = useState(false)
  const [feedback, setFeedback] = useState<Feedback>(null)
  const clearTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  // Until the player edits the box, keep it at the current minimum next bid.
  useEffect(() => {
    if (!dirty) setValue(String(a.min_next_bid))
  }, [a.min_next_bid, dirty])
  useEffect(() => () => clearTimeout(clearTimer.current), [])

  const show = (f: Feedback) => {
    clearTimeout(clearTimer.current)
    setFeedback(f)
    if (f) clearTimer.current = setTimeout(() => setFeedback(null), 6000)
  }

  const bid = useMutation({
    mutationFn: (amount: number) => api.placeBid(userId!, a.id, amount),
    onSuccess: async (res) => {
      setDirty(false)
      show({ kind: 'ok', text: `出價成功：${formatGold(res.amount)}` })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['auctions'] }),
        queryClient.invalidateQueries({ queryKey: ['me'] }),
        queryClient.invalidateQueries({ queryKey: ['inbox'] }),
      ])
    },
    onError: async (err) => {
      if (err instanceof ApiError && err.code === 'bid_too_low' && err.minNextBid !== undefined) {
        setValue(String(err.minNextBid))
        setDirty(false)
      }
      show({ kind: 'error', text: errorMessage(err) })
      // The price may have moved (or the auction ended): refresh the list as well.
      await queryClient.invalidateQueries({ queryKey: ['auctions'] })
    },
  })

  const submit = () => {
    const amount = parseAmount(value)
    if (amount === null) {
      show({ kind: 'error', text: '請輸入正整數金額' })
      return
    }
    bid.mutate(amount)
  }

  const wrongBracket = me !== undefined && me.level_bracket !== a.level_bracket
  const disabledReason = userId === null ? '請先選擇玩家' : wrongBracket ? `僅限 ${bracketLabel(a.level_bracket)} 的玩家` : null
  const disabled = disabledReason !== null || a.is_mine || bid.isPending

  return (
    <div className="space-y-1">
      <form
        className="flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <input
          type="text"
          inputMode="numeric"
          aria-label={`對 ${a.item.name} 的出價金額`}
          className="w-28 rounded border border-slate-300 px-2 py-1 text-right tabular-nums disabled:bg-slate-100"
          value={value}
          disabled={disabledReason !== null}
          onChange={(e) => {
            setValue(e.target.value)
            setDirty(true)
          }}
        />
        <button
          type="submit"
          disabled={disabled}
          className="rounded bg-indigo-600 px-3 py-1 text-sm font-medium text-white transition hover:bg-indigo-500 disabled:cursor-not-allowed disabled:bg-slate-300"
        >
          {bid.isPending ? '送出中…' : a.is_mine ? '領先中' : '出價'}
        </button>
      </form>
      {disabledReason && <div className="text-xs text-slate-400">{disabledReason}</div>}
      {feedback && (
        <div role="status" className={`text-xs ${feedback.kind === 'ok' ? 'text-emerald-600' : 'text-red-600'}`}>
          {feedback.text}
        </div>
      )}
    </div>
  )
}
