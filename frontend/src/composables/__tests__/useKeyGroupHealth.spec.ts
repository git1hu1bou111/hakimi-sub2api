import { mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getMatrix, type MonitorMatrixResponse, type MonitorMatrixRow } from '@/api/channelMonitorV2'
import { useKeyGroupHealth } from '@/composables/useKeyGroupHealth'

vi.mock('@/api/channelMonitorV2', () => ({ getMatrix: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
const auth = reactive({ token: 'session', user: { id: 1 }, isAuthenticated: true })
const matrix = vi.mocked(getMatrix)
const coverage = {
  requested_start: '2026-11-26T06:31:00Z', requested_end: '2026-11-26T08:01:00Z',
  coverage_start: '2026-11-26T06:31:00Z', data_through: '2026-11-26T08:00:00Z',
  computed_at: '2026-11-26T08:00:00Z', aggregation_lag_seconds: 0, coverage_complete: true, bucket_seconds: 300,
}
function row(group_id: number | undefined, platform: string): MonitorMatrixRow {
  return { group_id, platform, buckets: [] } as MonitorMatrixRow
}
function response(...items: MonitorMatrixRow[]): MonitorMatrixResponse {
  return { items, coverage, group_by: 'platform_group' }
}
function pendingResponse() {
  let resolve!: (value: MonitorMatrixResponse) => void
  const promise = new Promise<MonitorMatrixResponse>(done => { resolve = done })
  return { promise, resolve }
}
function persistUser(id: number) {
  localStorage.setItem('auth_user', JSON.stringify({ id }))
  localStorage.setItem('auth_token', 'session-' + id)
}

describe('useKeyGroupHealth', () => {
  let wrapper: VueWrapper | undefined
  let visibility: DocumentVisibilityState
  function setVisibility(value: DocumentVisibilityState) {
    visibility = value
    document.dispatchEvent(new Event('visibilitychange'))
  }
  function open(initial = true) {
    const active = ref(initial)
    let health!: ReturnType<typeof useKeyGroupHealth>
    wrapper = mount(defineComponent({
      setup() {
        health = useKeyGroupHealth(active)
        return () => null
      },
    }))
    return { active, health }
  }
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-26T08:03:00Z'))
    matrix.mockReset().mockResolvedValue(response())
    auth.user = { id: 1 }
    auth.isAuthenticated = true
    auth.token = 'session-1'
    persistUser(1)
    visibility = 'visible'
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
  })
  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    localStorage.clear()
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('loads one user matrix for all options and keeps each platform under its real group ID', async () => {
    const a = row(7, 'openai'), b = row(7, 'anthropic'), c = row(8, 'future-provider')
    matrix.mockResolvedValue(response(a, b, c, row(0, 'openai'), row(undefined, 'openai')))
    const { active, health } = open(false)
    await vi.advanceTimersByTimeAsync(120_000)
    expect(matrix).not.toHaveBeenCalled()
    setVisibility('hidden')
    active.value = true
    expect(matrix).not.toHaveBeenCalled()
    setVisibility('visible')
    expect(health.state.value).toBe('loading')
    await nextTick()
    expect(matrix).toHaveBeenCalledTimes(1)
    expect(matrix).toHaveBeenCalledWith(
      { range: '90m', platforms: [], groupIds: [], models: [] }, 'platform_group', false, expect.any(AbortSignal), true,
    )
    expect(health.rowsForGroup(7)).toEqual([a, b])
    expect(health.rowsForGroup(7, 'anthropic')).toEqual([b])
    expect(health.rowsForGroup(7, 'gemini')).toEqual([])
    expect(health.rowsForGroup(8)).toEqual([c])
    expect(health.rowsForGroup(0)).toEqual([])
    expect(health.rowsForGroup(9)).toEqual([])
    expect(health.coverage.value).toEqual(coverage)
    expect(health.receivedAt.value).toBe(Date.now())
    expect(health.state.value).toBe('ready')
  })

  it('shares a minute of cached data across opens, then refreshes only while visible and active', async () => {
    const { active, health } = open()
    await nextTick()
    const firstReceipt = health.receivedAt.value
    active.value = false
    await vi.advanceTimersByTimeAsync(59_000)
    active.value = true
    expect(health.receivedAt.value).toBe(firstReceipt)
    expect(matrix).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(matrix).toHaveBeenCalledTimes(2)
    expect(health.receivedAt.value).toBe(firstReceipt + 60_000)
    setVisibility('hidden')
    await vi.advanceTimersByTimeAsync(120_000)
    expect(matrix).toHaveBeenCalledTimes(2)
    setVisibility('visible')
    await nextTick()
    expect(matrix).toHaveBeenCalledTimes(3)
    active.value = false
    await vi.advanceTimersByTimeAsync(120_000)
    expect(matrix).toHaveBeenCalledTimes(3)
  })

  it('aborts on close, hide and unmount, discarding even a late successful response', async () => {
    const old = pendingResponse(), current = pendingResponse(), last = pendingResponse()
    matrix.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise).mockReturnValueOnce(last.promise)
    const { active, health } = open()
    active.value = false
    expect(matrix.mock.calls[0][3]?.aborted).toBe(true)
    active.value = true
    setVisibility('hidden')
    expect(matrix.mock.calls[1][3]?.aborted).toBe(true)
    setVisibility('visible')
    old.resolve(response(row(1, 'wrong-1')))
    current.resolve(response(row(1, 'wrong-2')))
    await nextTick()
    expect(health.state.value).toBe('loading')
    expect(health.rowsForGroup(1)).toEqual([])
    wrapper?.unmount()
    wrapper = undefined
    expect(matrix.mock.calls[2][3]?.aborted).toBe(true)
    last.resolve(response(row(1, 'wrong-3')))
    await vi.advanceTimersByTimeAsync(120_000)
    expect(health.coverage.value).toBeUndefined()
    expect(matrix).toHaveBeenCalledTimes(3)
  })

  it('keeps the last receipt and rows during refresh, then clears a failed snapshot and retries on reopen', async () => {
    let rejectRefresh!: (error: Error) => void
    matrix.mockResolvedValueOnce(response(row(2, 'openai'))).mockReturnValueOnce(
      new Promise((_resolve, reject) => { rejectRefresh = reject }),
    )
    const { active, health } = open()
    await nextTick()
    const receivedAt = health.receivedAt.value
    await vi.advanceTimersByTimeAsync(60_000)
    expect(health.state.value).toBe('ready')
    expect(health.rowsForGroup(2)).toEqual([row(2, 'openai')])
    expect(health.coverage.value).toEqual(coverage)
    expect(health.receivedAt.value).toBe(receivedAt)
    rejectRefresh(new Error('Unavailable'))
    await nextTick()
    expect(health.state.value).toBe('error')
    expect(health.rowsForGroup(2)).toEqual([])
    expect(health.receivedAt.value).toBe(0)
    active.value = false
    active.value = true
    await nextTick()
    expect(matrix).toHaveBeenCalledTimes(3)
    expect(health.state.value).toBe('ready')
    expect(health.rowsForGroup(2)).toEqual([])
  })

  it('clears cached rows across accounts and ignores a response after another tab logs out', async () => {
    const other = pendingResponse()
    matrix.mockResolvedValueOnce(response(row(2, 'private'))).mockReturnValueOnce(other.promise)
    const { health } = open()
    await nextTick()
    expect(health.rowsForGroup(2)).toHaveLength(1)
    persistUser(3)
    window.dispatchEvent(new StorageEvent('storage', { key: 'auth_user' }))
    expect(health.rowsForGroup(2)).toEqual([])
    expect(matrix).toHaveBeenCalledTimes(1)
    auth.user = { id: 3 }
    auth.token = 'session-3'
    await nextTick()
    expect(matrix).toHaveBeenCalledTimes(2)
    localStorage.clear()
    other.resolve(response(row(3, 'late')))
    await nextTick()
    expect(matrix.mock.calls[1][3]?.aborted).toBe(true)
    expect(health.rowsForGroup(3)).toEqual([])
    await vi.advanceTimersByTimeAsync(120_000)
    expect(matrix).toHaveBeenCalledTimes(2)
  })

  it('checks the current user before reusing a fresh cached snapshot', async () => {
    matrix.mockResolvedValueOnce(response(row(2, 'private')))
    const { active, health } = open()
    await nextTick()
    active.value = false
    persistUser(3)
    // Opening can precede delivery of the other tab's storage event.
    active.value = true
    expect(health.rowsForGroup(2)).toEqual([])
    expect(health.receivedAt.value).toBe(0)
    expect(matrix).toHaveBeenCalledTimes(1)
  })
})
