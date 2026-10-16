import type { Filters, SortKey } from '../types'
import { ITEM_TYPES, SORT_LABELS, bracketLabel, itemTypeLabel } from '../labels'

const BRACKETS = [0, 1, 2, 3, 4, 5]

interface Props {
  filters: Filters
  secondsSinceUpdate: number | null
  onChange: (f: Filters) => void
}

const selectClass = 'rounded border border-slate-300 bg-white px-2 py-1 text-sm'

export function FilterBar({ filters, secondsSinceUpdate, onChange }: Props) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg bg-white px-4 py-3 shadow-sm ring-1 ring-slate-200">
      <label className="flex items-center gap-2 text-sm">
        <span className="text-slate-500">等級區間</span>
        <select
          className={selectClass}
          value={String(filters.bracket)}
          onChange={(e) => onChange({ ...filters, bracket: e.target.value === 'all' ? 'all' : Number(e.target.value) })}
        >
          <option value="all">全部</option>
          {BRACKETS.map((b) => (
            <option key={b} value={b}>
              {bracketLabel(b)}
            </option>
          ))}
        </select>
      </label>

      <label className="flex items-center gap-2 text-sm">
        <span className="text-slate-500">物品類型</span>
        <select className={selectClass} value={filters.type} onChange={(e) => onChange({ ...filters, type: e.target.value })}>
          <option value="">全部</option>
          {ITEM_TYPES.map((t) => (
            <option key={t} value={t}>
              {itemTypeLabel(t)}
            </option>
          ))}
        </select>
      </label>

      <label className="flex items-center gap-2 text-sm">
        <span className="text-slate-500">排序</span>
        <select
          className={selectClass}
          value={filters.sort}
          onChange={(e) => onChange({ ...filters, sort: e.target.value as SortKey })}
        >
          {(Object.keys(SORT_LABELS) as SortKey[]).map((k) => (
            <option key={k} value={k}>
              {SORT_LABELS[k]}
            </option>
          ))}
        </select>
      </label>

      <span className="ml-auto text-sm text-slate-500" data-testid="updated-ago">
        {secondsSinceUpdate === null ? '載入中…' : `${secondsSinceUpdate} 秒前更新`}
      </span>
    </div>
  )
}
