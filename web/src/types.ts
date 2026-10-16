export type TimeBand = 'very_short' | 'short' | 'medium' | 'long' | 'very_long'

export interface Auction {
  id: number
  item: { name: string; type: string; level: number }
  level_bracket: number
  current_price: number
  min_next_bid: number
  top_bidder_name: string | null
  is_mine: boolean
  i_have_bid: boolean
  has_bids: boolean
  time_band: TimeBand
  status: string
}

export interface User {
  id: number
  name: string
  level: number
  level_bracket: number
}

export interface Me extends User {
  gold_available: number
  gold_frozen: number
  inbox_count: number
}

export interface InboxItem {
  id: number
  message: string
  created_at: string
  auction_id: number | null
  item: { id: number; name: string; type: string; level: number } | null
  price_paid: number | null
}

export interface BidResponse {
  bid_id: number
  auction_id: number
  amount: number
  current_price: number
  min_next_bid: number
  replayed: boolean
}

export type SortKey = 'ending' | 'price_asc' | 'price_desc' | 'level_desc'

export interface Filters {
  bracket: number | 'all'
  type: string // '' means all types
  sort: SortKey
}
