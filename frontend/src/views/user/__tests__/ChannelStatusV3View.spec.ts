import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
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
      if (key === 'channelMonitorV3.timelineNoData') return `${params?.time} · 无数据`
      if (key === 'channelMonitorV3.timelineTooltip') return `${params?.time} · 可用率 ${params?.availability}`
      return key === 'channelMonitorV3.groupCount' ? `${params?.count} 个分组` : labels[key] ?? key
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
  return { items, coverage, group_by: 'platform_group' }
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
    expect(api.getMatrix).toHaveBeenCalledWith(expect.anything(), 'platform_group', false, expect.any(AbortSignal))
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
      timelineWindow: { start: Date.parse(coverage.requested_start), length: 18, step: 300_000 },
    })
    expect(view.get('[data-platform="anthropic"]').getComponent(ChannelMonitorV3Card).props('row').intelligence).toBeUndefined()
  })

  it.each([true, false])('locates sparse buckets within the requested matrix window (requested end present: %s)', async includeEnd => {
    const channel = row('anthropic', 7)
    channel.buckets = ['01:20', '00:10', '00:00', '01:30'].map(time => ({
      bucket_start: `2026-09-25T${time}:00Z`, metrics: channel.metrics, health: channel.health,
    }))
    const matrix = response([channel, row('anthropic', 8)])
    matrix.coverage = {
      ...coverage, requested_end: includeEnd ? '2026-09-25T01:30:00Z' : undefined,
      data_through: '2026-09-25T01:24:00Z', coverage_complete: false,
    }
    vi.mocked(api.getMatrix).mockResolvedValue(matrix)
    const view = render(false)
    await flushPromises()
    const [sparse, empty] = view.findAllComponents(ChannelMonitorV3Card)
    const slots = sparse.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(18)
    const sampled = slots.flatMap((slot, i) => slot.attributes('aria-label')?.includes('可用率') ? [i] : [])
    expect(sampled).toEqual([0, 2, 16])
    expect(empty.findAll('.v3-bar-hitbox')).toHaveLength(18)
    expect(empty.findAll('.v3-bar-hitbox').every(slot => slot.attributes('aria-label').includes('无数据'))).toBe(true)
    const label = new Intl.DateTimeFormat('zh-CN', {
      month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    }).format(new Date('2026-09-25T01:25:00Z')) + ' · 无数据'
    expect(slots[17].attributes('aria-label')).toBe(label)
    expect(slots[17].get('.v3-soft-glass-bar').classes()).toContain('bg-gray-300')
    await slots[17].trigger('focus')
    expect(document.body.querySelector('[role="tooltip"]')?.textContent?.trim()).toBe(label)
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
    expect(slots[1].attributes('aria-label')).toContain('可用率')
    expect(slots[length - 1].attributes('aria-label')).toContain('无数据')
  })

  it('aligns non-boundary windows to UTC bucket starts and ignores out-of-range rows', async () => {
    const channel = row('anthropic', 7)
    channel.buckets = ['08:00', '08:05', '09:30', '09:35'].map(time => ({
      bucket_start: `2026-09-25T${time}:00+08:00`, metrics: channel.metrics, health: channel.health,
    }))
    vi.mocked(api.getMatrix).mockResolvedValue({
      ...response([channel]),
      coverage: { ...coverage, requested_start: '2026-09-25T00:02:00Z', requested_end: '2026-09-25T01:32:00Z' },
    })
    const view = render(false)
    await flushPromises()
    const slots = view.findAll('.v3-bar-hitbox')
    expect(slots).toHaveLength(18)
    expect(slots.flatMap((slot, i) => slot.attributes('aria-label')?.includes('可用率') ? [i] : [])).toEqual([0, 17])
  })

  it.each([true, false])('anchors missing starts using the matrix end or receipt time (end present: %s)', async includeEnd => {
    const receivedAt = includeEnd ? '2026-09-27T18:03:00Z' : '2026-09-25T01:26:00Z'
    const now = vi.spyOn(Date, 'now').mockReturnValue(Date.parse(receivedAt))
    try {
      const channel = row('anthropic', 7)
      channel.buckets = [{ bucket_start: '2026-09-25T00:10:00Z', metrics: channel.metrics, health: channel.health }]
      vi.mocked(api.getMatrix).mockResolvedValue({
        ...response([channel]), coverage: { ...coverage, requested_start: '', requested_end: includeEnd ? '2026-09-25T01:30:00Z' : undefined },
      })
      const view = render(false)
      await flushPromises()
      const slots = view.findAll('.v3-bar-hitbox')
      expect(slots).toHaveLength(18)
      expect(slots[2].attributes('aria-label')).toContain('可用率')
      expect(slots[17].attributes('aria-label')).toContain('无数据')
    } finally {
      now.mockRestore()
    }
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
