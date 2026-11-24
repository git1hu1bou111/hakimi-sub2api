import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import messages from '@/i18n/locales/zh/channelMonitorV3'
import type { MonitorMatrixResponse, MonitorMatrixRow, MonitorSnapshot } from '@/api/channelMonitorV2'
import * as api from '@/api/channelMonitorV2'
import userGroupsAPI from '@/api/groups'
import ChannelStatusV3View from '../ChannelStatusV3View.vue'
import ChannelMonitorV3Card from '@/components/user/monitor/ChannelMonitorV3Card.vue'

vi.mock('@/api/channelMonitorV2', () => ({
  getSnapshot: vi.fn(),
  getMatrix: vi.fn(),
}))
vi.mock('@/api/groups', () => ({
  default: { getAvailable: vi.fn(), getUserGroupRates: vi.fn() },
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    locale: { value: 'zh-CN' },
    t: (key: string, params?: Record<string, unknown>) => {
      const labels: Record<string, string> = {
        'monitorCommon.providers.openai': 'OpenAI',
        'monitorCommon.providers.anthropic': 'Anthropic',
        'monitorCommon.providers.grok': 'Grok',
        'monitorCommon.providers.gemini': 'Gemini',
      }
      const message = messages.channelMonitorV3[key.split('.')[1] as keyof typeof messages.channelMonitorV3]
      if (key.startsWith('channelMonitorV3.') && typeof message === 'string') {
        return message.replace(/\{(\w+)\}/g, (_, name: string) => String(params?.[name]))
      }
      return labels[key] ?? key
    },
  }),
}))

const coverage = {
  requested_start: '2026-09-25T00:00:00Z',
  coverage_start: '2026-09-25T00:00:00Z',
  data_through: '2026-09-25T01:30:00Z',
  computed_at: '2026-09-25T01:30:00Z',
  aggregation_lag_seconds: 0,
  coverage_complete: true,
  bucket_seconds: 300,
}
function stamp(time: string) { return `2026-09-25T${time}:00Z` }
function label(time: string) {
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(stamp(time)))
}
function clock(time: string) {
  return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit' }).format(new Date(stamp(time)))
}
const trailingCoverage = {
  ...coverage, requested_start: stamp('01:26'), requested_end: stamp('02:56'),
  coverage_start: stamp('01:26'), data_through: stamp('02:55'), computed_at: stamp('02:55'),
}
function row(platform: string, groupId?: number): MonitorMatrixRow {
  return {
    platform,
    group_id: groupId,
    group_name: `${platform}-${groupId}`,
    metrics: { cache_rate: 0.8, error_rate: 0, ttft: { p50_ms: 1000 } },
    health: { overall: 'healthy' },
    buckets: [],
  } as MonitorMatrixRow
}
function response(items: MonitorMatrixRow[]): MonitorMatrixResponse {
  return { items, coverage: trailingCoverage, group_by: 'platform_group' }
}

describe('Channel status platform sections', () => {
  let wrapper: VueWrapper | undefined

  beforeEach(() => {
    vi.resetAllMocks()
    vi.mocked(api.getSnapshot).mockResolvedValue({
      coverage, config: { refresh_interval_seconds: 60 }, trend: [],
    } as unknown as MonitorSnapshot)
    vi.mocked(api.getMatrix).mockResolvedValue(response([]))
    vi.mocked(userGroupsAPI.getAvailable).mockResolvedValue([])
    vi.mocked(userGroupsAPI.getUserGroupRates).mockResolvedValue({})
  })
  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
  })
  function render(stubCards = true) {
    wrapper = mount(ChannelStatusV3View, {
      global: { stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        Icon: true,
        EmptyState: true,
        ProviderIcon: true,
        ChannelMonitorV3Card: stubCards,
      } },
    })
    return wrapper
  }

  it('uses separate grids in OpenAI / Anthropic / Grok / Gemini order', async () => {
    vi.mocked(api.getMatrix).mockResolvedValue(response([
      row('gemini', 1), row('grok', 2), row('anthropic', 3),
      row('openai', 10), row('openai', 8), row('openai', 7),
      row('openai', 5), row('openai', 4),
    ]))
    const view = render()
    await flushPromises()
    const sections = view.findAll('section[data-platform]')
    expect(sections.map(s => s.attributes('data-platform'))).toEqual(['openai', 'anthropic', 'grok', 'gemini'])
    expect(sections.map(s => s.findAllComponents(ChannelMonitorV3Card).length)).toEqual([5, 1, 1, 1])
    expect(sections[0].get('h2').text()).toMatch(/OpenAI\s*5/)
    expect(sections[0].get('[aria-label="5 个分组"]').exists()).toBe(true)
    expect(sections[0].findAllComponents(ChannelMonitorV3Card).map(c => c.props('row').group_id)).toEqual([4, 5, 7, 8, 10])
    for (const section of sections) {
      expect(section.get('.grid.grid-cols-1').classes()).toEqual(expect.arrayContaining(['md:grid-cols-2', 'xl:grid-cols-3', '2xl:grid-cols-4']))
      expect(section.attributes('aria-labelledby')).toBe(section.get('h2').attributes('id'))
    }
    expect(api.getMatrix).toHaveBeenCalledWith(expect.anything(), 'platform_group', false, expect.any(AbortSignal), true)
  })

  it('retains other and unknown platforms once without creating empty sections', async () => {
    const items = [row('new-provider', 1), row('deepseek', 2), row('openai', 3), row('kimi', 4)]
    vi.mocked(api.getMatrix).mockResolvedValue(response(items))
    const view = render()
    await flushPromises()
    expect(view.findAll('section[data-platform]').map(s => s.attributes('data-platform'))).toEqual(['openai', 'domestic-models', 'new-provider'])
    expect(view.get('[data-platform="new-provider"] h2').text()).toContain('new-provider')
    const ids = view.findAllComponents(ChannelMonitorV3Card).map(c => c.props('row').group_id)
    expect(ids.sort()).toEqual([1, 2, 3, 4])
  })

  it('filters aggregate and invalid group rows without mutating the response', async () => {
    const items = [row('openai', 9), row('anthropic'), row('grok', 0), row('gemini', -1), row('openai', 1)]
    vi.mocked(api.getMatrix).mockResolvedValue(response(items))
    const view = render()
    await flushPromises()
    expect(view.findAll('section[data-platform]')).toHaveLength(1)
    expect(view.findAllComponents(ChannelMonitorV3Card).map(c => c.props('row').group_id)).toEqual([1, 9])
    expect(items.map(r => r.group_id)).toEqual([9, undefined, 0, -1, 1])
  })

  it('preserves intelligence data, coverage, countdown and user rates on the cards', async () => {
    const openai = row('openai', 52)
    openai.intelligence = {
      model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60,
      timeout_seconds: 30, status: 'green', points: [],
    }
    vi.mocked(api.getMatrix).mockResolvedValue(response([row('anthropic', 70), openai]))
    vi.mocked(userGroupsAPI.getAvailable).mockResolvedValue([{ id: 52, rate_multiplier: 2 }] as never)
    vi.mocked(userGroupsAPI.getUserGroupRates).mockResolvedValue({ 52: 1.5 })
    const view = render()
    await flushPromises()
    const card = view.get('[data-platform="openai"]').getComponent(ChannelMonitorV3Card)
    expect(card.props('row')).toEqual(openai)
    expect(card.props()).toMatchObject({
      coverage, countdownSeconds: 60, userRateMultiplier: 1.5,
      timelineWindow: { start: Date.parse(trailingCoverage.requested_start), length: 18, step: 300_000 },
    })
    expect(view.get('[data-platform="anthropic"]').getComponent(ChannelMonitorV3Card).props('row').intelligence).toBeUndefined()
  })

  it('shows 02:51–02:54 at the 02:55 point, then adds 02:55 and advances to the adjacent 03:00 window', async () => {
    const channel = row('anthropic', 7)
    const bucket = (time: string, errorRate = 0) => ({
      bucket_start: stamp(time), metrics: { ...channel.metrics, error_rate: errorRate }, health: channel.health,
    })
    channel.buckets = [bucket('02:51', 1), bucket('01:26'), bucket('02:46'), bucket('01:21'), bucket('02:56')]
    vi.mocked(api.getMatrix).mockResolvedValue(response([channel, row('anthropic', 8)]))
    const view = render(false)
    await flushPromises()
    const [sparse, empty] = view.findAllComponents(ChannelMonitorV3Card)
    const slots = sparse.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(18)
    expect(slots.flatMap((slot, i) => slot.attributes('aria-label').includes('可用率') ? [i] : [])).toEqual([0, 16, 17])
    const last = () => sparse.findAll('.v3-bar-hitbox')[17]
    const interval = (point: string, from: string, last: string) =>
      `${label(point)} · 已统计 ${clock(from)}～${clock(last)}`
    expect(slots[16].attributes('aria-label')).toContain(interval('02:50', '02:46', '02:50'))
    expect(last().attributes('aria-label')).toContain(interval('02:55', '02:51', '02:54') + ' · 可用率 0.00%')
    expect(last().get('.v3-soft-glass-bar').classes()).toContain('bg-gray-950')
    expect(empty.findAll('.v3-bar-hitbox')[17].attributes('aria-label')).toBe(`${label('02:55')} · 自 ${clock('02:51')} 起，截至 ${clock('02:54')} 暂无请求`)
    expect(empty.get('.grid.grid-cols-3').text()).toBe('缓存率—可用率—首 Token—')
    expect(sparse.get('.grid.grid-cols-3').text()).toContain('可用率0.00%')
    expect(sparse.text()).toContain(`最近统计点 ${label('02:55')}`)
    await last().trigger('focus')
    expect(document.body.querySelector('[role="tooltip"]')?.textContent?.trim()).toBe(last().attributes('aria-label'))

    const refresh = async (start: string, end: string, through: string) => {
      vi.mocked(api.getMatrix).mockResolvedValue({
        ...response([{ ...channel, buckets: [bucket('02:51', 0.2), bucket('02:56')] }]),
        coverage: { ...trailingCoverage, requested_start: stamp(start), requested_end: stamp(end), data_through: stamp(through) },
      })
      await view.get('button[title="common.refresh"]').trigger('click')
      await flushPromises()
    }
    await refresh('01:26', '02:56', '02:56')
    expect(last().attributes('aria-label')).toContain(interval('02:55', '02:51', '02:55') + ' · 可用率 80.0%')
    await refresh('01:31', '03:01', '03:00')
    expect(sparse.findAll('.v3-bar-hitbox')[16].attributes('aria-label')).toContain(interval('02:55', '02:51', '02:55'))
    expect(last().attributes('aria-label')).toContain(interval('03:00', '02:56', '02:59') + ' · 可用率 100.0%')
  })

  it.each([
    ['24h', 3600, 24], ['7d', 43200, 14], ['30d', 86400, 30],
  ] as const)('changes the timeline to %s only after its matrix arrives', async (range, step, length) => {
    const channel = row('anthropic', 7)
    vi.mocked(api.getMatrix).mockResolvedValueOnce(response([channel]))
    const view = render(false)
    await flushPromises()
    let finishMatrix!: (matrix: MonitorMatrixResponse) => void
    vi.mocked(api.getMatrix).mockReturnValueOnce(new Promise(resolve => { finishMatrix = resolve }))
    const button = view.findAll('button').find(item => item.text() === `channelMonitorV3.ranges.${range}`)!
    await button.trigger('click')
    expect(view.findAll('.v3-bar-slot')).toHaveLength(18)
    const start = Date.parse(coverage.requested_start)
    const second = new Date(start + step * 1000).toISOString()
    finishMatrix({
      ...response([{ ...channel, buckets: [{ bucket_start: second, metrics: channel.metrics, health: channel.health }] }]),
      coverage: { ...coverage, bucket_seconds: step, requested_end: new Date(start + length * step * 1000).toISOString() },
    })
    await flushPromises()
    const slots = view.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(length)
    expect(api.getMatrix).toHaveBeenLastCalledWith(expect.objectContaining({ range }), 'platform_group', false, expect.any(AbortSignal), false)
    expect(slots[1].attributes('aria-label')).toContain('可用率')
    expect(slots[length - 1].attributes('aria-label')).toContain('无数据')
  })

  it('limits empty and partially covered slots to actual coverage and shows the server-reported lag', async () => {
    const channel = row('anthropic', 7)
    vi.mocked(api.getMatrix).mockResolvedValue({
      ...response([channel]), coverage: { ...trailingCoverage, coverage_start: stamp('01:33'), aggregation_lag_seconds: 612, coverage_complete: false },
    })
    const view = render(false)
    await flushPromises()
    const slots = view.findAll('.v3-bar-hitbox')
    expect(slots[0].attributes('aria-label')).toBe(`${label('01:30')} · 此时段尚未覆盖`)
    expect(slots[1].attributes('aria-label')).toBe(`${label('01:35')} · 自 ${clock('01:33')} 起，截至 ${clock('01:35')} 暂无请求`)
    expect(view.text()).toContain('汇总延迟 10 分钟')
    expect(view.text()).toContain('部分历史覆盖')
    expect(view.text()).toContain(`最近统计点 ${label('02:55')}`)
  })

  it('shows initialization separately even if the server supplies a placeholder watermark', async () => {
    vi.mocked(api.getMatrix).mockResolvedValue({
      ...response([row('anthropic', 7)]),
      coverage: { ...trailingCoverage, computed_at: '0001-01-01T00:00:00Z', data_through: trailingCoverage.requested_start },
    })
    const view = render(false)
    await flushPromises()
    const slots = view.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(18)
    expect(slots.every(s => s.attributes('aria-label').includes('等待首次汇总'))).toBe(true)
    expect(view.text()).not.toContain('更新至')
  })

  it('shows dates for minutes on the other side of local midnight', async () => {
    const point = new Date(2026, 8, 25, 0, 0).getTime()
    const minute = 60_000
    const iso = (offset: number) => new Date(point + offset * minute).toISOString()
    const fullTime = (offset: number) => new Intl.DateTimeFormat('zh-CN', {
      month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    }).format(new Date(iso(offset)))
    const channel = row('anthropic', 7)
    channel.buckets = [{ bucket_start: iso(-4), metrics: channel.metrics, health: channel.health }]
    vi.mocked(api.getMatrix).mockResolvedValue({
      ...response([channel]),
      coverage: { ...trailingCoverage, requested_start: iso(-89), requested_end: iso(1), coverage_start: iso(-89), data_through: iso(0) },
    })
    const view = render(false)
    await flushPromises()
    expect(view.findAll('.v3-bar-hitbox')[17].attributes('aria-label')).toContain(`${fullTime(0)} · 已统计 ${fullTime(-4)}～${fullTime(-1)}`)
  })

  it('uses the actual requested end when a trailing start is missing', async () => {
    const channel = row('anthropic', 7)
    channel.buckets = [{ bucket_start: stamp('01:36'), metrics: channel.metrics, health: channel.health }]
    vi.mocked(api.getMatrix).mockResolvedValue({
      ...response([channel]), coverage: { ...trailingCoverage, requested_start: '' },
    })
    const view = render(false)
    await flushPromises()
    const slots = view.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(18)
    expect(slots[2].attributes('aria-label')).toContain(`${label('01:40')} · 已统计`)
    expect(view.text()).toContain(`上方指标取自 ${label('01:40')} 统计点`)
  })

  it('does not reinterpret old five-minute buckets as trailing-minute measurements', async () => {
    const channel = row('anthropic', 7)
    channel.buckets = [{ bucket_start: stamp('01:25'), metrics: channel.metrics, health: channel.health }]
    vi.mocked(api.getMatrix).mockResolvedValue({ ...response([channel]), coverage })
    const view = render(false)
    await flushPromises()
    const slots = view.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(18)
    expect(slots.every(s => s.attributes('aria-label') === '当前响应缺少五分钟回溯窗口')).toBe(true)
  })

  it('re-groups and updates counts after refreshing', async () => {
    vi.mocked(api.getMatrix).mockResolvedValueOnce(response([row('openai', 1), row('grok', 2)]))
    const view = render()
    await flushPromises()
    vi.mocked(api.getMatrix).mockResolvedValueOnce(response([row('anthropic', 2), row('openai', 1), row('openai', 3)]))
    await view.get('button[title="common.refresh"]').trigger('click')
    await flushPromises()
    expect(view.findAll('section[data-platform]').map(s => s.attributes('data-platform'))).toEqual(['openai', 'anthropic'])
    expect(view.get('[data-platform="openai"] h2').text()).toMatch(/OpenAI\s*2/)
    expect(view.findAllComponents(ChannelMonitorV3Card)).toHaveLength(3)
  })

  it('does not display empty platform headings for loading or empty data', async () => {
    const view = render()
    await nextTick()
    expect(view.findAll('section[data-platform]')).toHaveLength(0)
    expect(view.findAll('.animate-pulse')).toHaveLength(8)
    await flushPromises()
    expect(view.findAll('section[data-platform]')).toHaveLength(0)
    expect(view.findComponent({ name: 'EmptyState' }).exists()).toBe(true)
  })

  it('refreshes every 60 seconds even when passive monitoring requests 300', async () => {
    vi.mocked(api.getSnapshot).mockResolvedValue({
      coverage, config: { refresh_interval_seconds: 300 }, trend: [],
    } as unknown as MonitorSnapshot)
    vi.mocked(api.getMatrix).mockResolvedValue(response([row('openai', 1)]))
    const interval = vi.spyOn(window, 'setInterval')
    const view = render()
    await flushPromises()
    expect(view.getComponent(ChannelMonitorV3Card).props('countdownSeconds')).toBe(60)
    expect(interval).toHaveBeenCalledWith(expect.any(Function), 60000)
    expect(interval).not.toHaveBeenCalledWith(expect.any(Function), 300000)
    interval.mockRestore()
  })
})
