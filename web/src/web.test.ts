import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, auctionsPath, newIdempotencyKey } from './api'
import { parseAmount, secondsAgo } from './format'
import { bracketLabel, errorMessage } from './labels'

afterEach(() => vi.unstubAllGlobals())

function stubFetch(status: number, body: unknown) {
  const fn = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', fn)
  return fn
}

describe('format helpers', () => {
  it('parses only positive integers', () => {
    expect(parseAmount('1050')).toBe(1050)
    expect(parseAmount(' 1,050 ')).toBe(1050)
    for (const bad of ['', '0', '-5', '10.5', 'abc', '1e3']) expect(parseAmount(bad)).toBeNull()
  })
  it('computes whole seconds since the last update', () => {
    expect(secondsAgo(1000, 4500)).toBe(3)
    expect(secondsAgo(5000, 4000)).toBe(0)
  })
  it('labels level brackets', () => expect(bracketLabel(2)).toBe('20–29 級'))
})

describe('error messages', () => {
  it('maps API error codes to Chinese messages', () => {
    expect(errorMessage(new ApiError(409, 'bid_too_low', 'x', 1103))).toContain('1,103')
    expect(errorMessage(new ApiError(409, 'already_top_bidder', 'x'))).toBe('你已經是最高出價者')
    expect(errorMessage(new ApiError(410, 'auction_ended', 'x'))).toBe('拍賣已結束')
    expect(errorMessage(new ApiError(422, 'insufficient_gold', 'x'))).toBe('可用金幣不足')
    expect(errorMessage(new ApiError(422, 'level_bracket_mismatch', 'x'))).toContain('等級區間')
    expect(errorMessage(new ApiError(404, 'auction_not_found', 'x'))).toBe('找不到這場拍賣')
    expect(errorMessage(new ApiError(0, 'network_error', 'x'))).toContain('網路')
    expect(errorMessage(new ApiError(500, 'weird', 'x'))).toContain('weird')
    expect(errorMessage(new Error('boom'))).toContain('未知')
  })
})

describe('api client', () => {
  it('builds the auction list query', () => {
    expect(auctionsPath({ bracket: 2, type: 'ring', sort: 'price_asc' })).toBe('/api/auctions?bracket=2&type=ring&sort=price_asc')
    expect(auctionsPath({ bracket: 'all', type: '', sort: 'ending' })).toBe('/api/auctions?sort=ending')
  })

  it('generates a fresh idempotency key for every bid', async () => {
    const fetchMock = stubFetch(200, { bid_id: 1, auction_id: 7, amount: 1000, current_price: 1000, min_next_bid: 1050, replayed: false })
    await api.placeBid(3, 7, 1000)
    await api.placeBid(3, 7, 1000)
    const keys = fetchMock.mock.calls.map((c) => (c[1] as RequestInit).headers as Record<string, string>).map((h) => h['Idempotency-Key'])
    expect(keys).toHaveLength(2)
    expect(keys[0]).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(keys[0]).not.toBe(keys[1])
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect((init.headers as Record<string, string>)['X-Dev-User-Id']).toBe('3')
    expect(init.body).toBe('{"amount":1000}')
  })

  it('exposes min_next_bid from a 409 bid_too_low response', async () => {
    stubFetch(409, { error: { code: 'bid_too_low', message: 'too low', min_next_bid: 1103 } })
    const err = await api.placeBid(1, 1, 5).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 409, code: 'bid_too_low', minNextBid: 1103 })
  })

  it('turns network failures into an ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('failed') }))
    await expect(api.me(1)).rejects.toMatchObject({ code: 'network_error' })
  })

  it('newIdempotencyKey is unique', () => {
    expect(new Set(Array.from({ length: 50 }, newIdempotencyKey)).size).toBe(50)
  })
})
