import type { InboxItem } from '../types'
import { formatDateTime, formatGold } from '../format'
import { itemTypeLabel } from '../labels'

export function InboxPanel({ items }: { items: InboxItem[] }) {
  return (
    <section className="rounded-lg bg-white shadow-sm ring-1 ring-slate-200">
      <h2 className="border-b border-slate-100 px-4 py-3 text-base font-semibold">包裹</h2>
      {items.length === 0 ? (
        <p className="px-4 py-6 text-center text-sm text-slate-400">目前沒有物品，去拍賣行得標吧！</p>
      ) : (
        <ul className="divide-y divide-slate-100" data-testid="inbox-list">
          {items.map((it) => (
            <li key={it.id} className="flex flex-wrap items-baseline gap-x-6 gap-y-1 px-4 py-3 text-sm">
              <div className="min-w-40">
                <span className="font-medium">{it.item?.name ?? '（物品）'}</span>
                {it.item && (
                  <span className="ml-2 text-xs text-slate-500">
                    {itemTypeLabel(it.item.type)} · {it.item.level} 級
                  </span>
                )}
              </div>
              <div className="tabular-nums text-slate-600">
                花費 <span className="font-semibold">{it.price_paid === null ? '—' : formatGold(it.price_paid)}</span> 金幣
              </div>
              <div className="ml-auto text-xs text-slate-400">{formatDateTime(it.created_at)}</div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
