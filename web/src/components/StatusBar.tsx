import type { Me, User } from '../types'
import { formatGold } from '../format'

interface Props {
  users: User[]
  userId: number | null
  me: Me | undefined
  onSelectUser: (id: number) => void
}

export function StatusBar({ users, userId, me, onSelectUser }: Props) {
  return (
    <header className="border-b border-slate-200 bg-white shadow-sm">
      <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-6 gap-y-3 px-4 py-3">
        <div className="flex items-center gap-2">
          <h1 className="text-xl font-bold tracking-wide">拍賣行</h1>
          <span className="rounded bg-amber-100 px-2 py-0.5 text-xs font-semibold text-amber-800 ring-1 ring-amber-200">
            開發模式
          </span>
        </div>

        <label className="flex items-center gap-2 text-sm">
          <span className="text-slate-500">玩家</span>
          <select
            className="rounded border border-slate-300 bg-white px-2 py-1"
            value={userId ?? ''}
            onChange={(e) => onSelectUser(Number(e.target.value))}
            aria-label="切換玩家"
          >
            {users.map((u) => (
              <option key={u.id} value={u.id}>
                {u.name}（{u.level} 級）
              </option>
            ))}
          </select>
        </label>

        <dl className="ml-auto flex flex-wrap items-center gap-x-6 gap-y-1 text-sm">
          <div className="flex items-baseline gap-1.5">
            <dt className="text-slate-500">可用金幣</dt>
            <dd className="font-semibold tabular-nums text-emerald-700" data-testid="gold-available">
              {me ? formatGold(me.gold_available) : '—'}
            </dd>
          </div>
          <div className="flex items-baseline gap-1.5">
            <dt className="text-slate-500">凍結金幣</dt>
            <dd className="font-semibold tabular-nums text-sky-700" data-testid="gold-frozen">
              {me ? formatGold(me.gold_frozen) : '—'}
            </dd>
          </div>
          <div className="flex items-baseline gap-1.5">
            <dt className="text-slate-500">包裹</dt>
            <dd className="font-semibold tabular-nums" data-testid="inbox-count">
              {me ? me.inbox_count : '—'}
            </dd>
          </div>
        </dl>
      </div>
    </header>
  )
}
