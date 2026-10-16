import type { SortKey, TimeBand } from './types'
import { ApiError } from './api'

export const ITEM_TYPE_LABELS: Record<string, string> = {
  weapon: '武器',
  shield: '盾牌',
  armor: '護甲',
  helmet: '頭盔',
  gloves: '手套',
  boots: '靴子',
  ring: '戒指',
  amulet: '護身符',
}

export const ITEM_TYPES = Object.keys(ITEM_TYPE_LABELS)

export const SORT_LABELS: Record<SortKey, string> = {
  ending: '結標時間（近→遠）',
  price_asc: '價格（低→高）',
  price_desc: '價格（高→低）',
  level_desc: '物品等級（高→低）',
}

export const TIME_BAND_LABELS: Record<TimeBand, string> = {
  very_short: '非常短',
  short: '短',
  medium: '中等',
  long: '長',
  very_long: '非常長',
}

/** Tailwind classes for the time band badge: red, yellow, then grey for the rest. */
export const TIME_BAND_STYLES: Record<TimeBand, string> = {
  very_short: 'bg-red-100 text-red-700 ring-red-200',
  short: 'bg-yellow-100 text-yellow-800 ring-yellow-200',
  medium: 'bg-slate-100 text-slate-600 ring-slate-200',
  long: 'bg-slate-100 text-slate-600 ring-slate-200',
  very_long: 'bg-slate-100 text-slate-600 ring-slate-200',
}

export function itemTypeLabel(type: string): string {
  return ITEM_TYPE_LABELS[type] ?? type
}

export function bracketLabel(bracket: number): string {
  return `${bracket * 10}–${bracket * 10 + 9} 級`
}

/** Chinese message for a failed request. */
export function errorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return '發生未知錯誤，請稍後再試'
  switch (err.code) {
    case 'bid_too_low':
      return err.minNextBid !== undefined
        ? `出價過低，最低下一口價已更新為 ${err.minNextBid.toLocaleString('zh-TW')}`
        : '出價過低'
    case 'already_top_bidder':
      return '你已經是最高出價者'
    case 'auction_ended':
      return '拍賣已結束'
    case 'insufficient_gold':
      return '可用金幣不足'
    case 'level_bracket_mismatch':
      return '你只能對自己等級區間的拍賣出價'
    case 'auction_not_found':
      return '找不到這場拍賣'
    case 'unauthorized':
      return '請先選擇玩家'
    case 'missing_idempotency_key':
    case 'invalid_request':
      return '請求格式錯誤'
    case 'network_error':
      return '網路連線失敗，請稍後再試'
    default:
      return `發生錯誤（${err.code}）`
  }
}
