import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import IntelligenceUptime from '../IntelligenceUptime.vue'
import ChannelMonitorV3Card from '../ChannelMonitorV3Card.vue'
import type { IntelligenceStatus, IntelligenceUptime as Uptime, MonitorMatrixRow } from '@/api/channelMonitorV2'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }),
}))

describe('Intelligence uptime', () => {
  beforeEach(() => { vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-09-25T01:00:00Z')) })
  afterEach(() => vi.restoreAllMocks())
  it.each([false, true])('uses group failure wording only for group cards (compact=%s)', compact => {
    const wrapper = mount(IntelligenceUptime, { props: {
      compact,
      uptime: { model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60,
        timeout_seconds: 30, status: 'yellow', points: [
          { checked_at: '2026-09-25T00:59:10Z', status: 'yellow', latency_ms: 1000 },
        ] },
    } })
    expect(wrapper.get('[role="status"]').text()).toContain(compact ? 'intelligence.yellow' : 'intelligence.groupYellow')
    expect(wrapper.text().includes('intelligence.groupLegend')).toBe(!compact)
  })
  it('defaults to a 60-minute window and fits all real minute records', () => {
    const wrapper = mount(IntelligenceUptime, { props: {
      uptime: { model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60,
        timeout_seconds: 30, status: 'green',
        points: Array.from({ length: 60 }, (_, minute) => ({
          checked_at: `2026-09-25T00:${String(minute).padStart(2, '0')}:10Z`,
          status: 'green' as const, latency_ms: 1000,
        })),
      },
    } })
    expect(wrapper.findAll('[role="img"]')).toHaveLength(60)
    expect(wrapper.get('.grid').attributes('style')).toContain('repeat(60, minmax(0, 1fr))')
    expect(wrapper.get('.grid').classes()).toContain('gap-px')
  })

  it('keeps intelligence length separate from the passive 18-slot card', () => {
    const row = { platform: 'openai', group_id: 7, group_name: 'Example', metrics: { cache_rate: 0, error_rate: 0, ttft: { p50_ms: null } }, health: { overall: 'unknown' }, buckets: [] } as unknown as MonitorMatrixRow
    const wrapper = mount(ChannelMonitorV3Card, { props: { row, countdownSeconds: 60, timelineLength: 18 },
      global: { stubs: { ProviderIcon: true, ChannelMonitorV3Timeline: true } } })
    expect(wrapper.getComponent(IntelligenceUptime).props('length')).toBe(60)
  })
  it.each(['green', 'yellow', 'red', 'stale'] as IntelligenceStatus[])('renders %s with no fabricated history', (status) => {
    const wrapper = mount(IntelligenceUptime, { props: {
      uptime: { model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30, status: status as IntelligenceStatus, points: [] },
    } })
    expect(wrapper.get('[role="status"]').text()).toContain('unknown')
    expect(wrapper.get('[role="status"] i').classes()).toContain('bg-gray-200')
    expect(wrapper.findAll('[role="img"]')).toHaveLength(0)
    expect(wrapper.text()).toContain('channelMonitorV3.intelligence.noData')
    expect(wrapper.text()).toContain('gpt-6-astra')
  })
  it('hides missing intervals and expands real samples across the row', () => {
    const wrapper = mount(IntelligenceUptime, { props: {
      length: 3,
      coverage: { requested_start: '2026-09-25T00:00:00Z', bucket_seconds: 300 } as never,
      uptime: { model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30, status: 'yellow',
        points: [{ checked_at: '2026-09-25T00:06:00Z', status: 'yellow', latency_ms: 1234 }] },
    } })
    const bars = wrapper.findAll('[role="img"]')
    expect(bars).toHaveLength(1)
    expect(bars[0].classes()).toContain('bg-amber-400')
    expect(bars[0].attributes('aria-label')).toContain('1.2s')
    expect(wrapper.get('.grid').attributes('style')).toContain('repeat(1, minmax(0, 1fr))')
  })
  it.each(['openai', 'anthropic', 'grok', 'gemini'])('only adds the row to OpenAI cards (%s)', platform => {
    const row = { platform, group_id: 7, group_name: 'Example', metrics: { cache_rate: 0, error_rate: 0, ttft: { p50_ms: null } }, health: { overall: 'unknown' }, buckets: [] } as unknown as MonitorMatrixRow
    const wrapper = mount(ChannelMonitorV3Card, { props: { row, countdownSeconds: 60, timelineLength: 18 },
      global: { stubs: { ProviderIcon: true, ChannelMonitorV3Timeline: true, IntelligenceUptime: true } } })
    expect(wrapper.findComponent(IntelligenceUptime).exists()).toBe(platform === 'openai')
  })
  it('uses independent one-minute bars instead of passive monitor buckets', () => {
    const wrapper = mount(IntelligenceUptime, { props: {
      length: 30,
      coverage: { requested_start: '2026-08-25T00:00:00Z', bucket_seconds: 86400 } as never,
      uptime: {
        model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30,
        status: 'green', window_start: '2026-09-25T00:00:00Z', bucket_seconds: 60, window_points: 60,
        points: [
          { checked_at: '2026-09-25T00:00:10Z', status: 'red', latency_ms: 1000 },
          { checked_at: '2026-09-25T00:01:10Z', status: 'yellow', latency_ms: 1000 },
          { checked_at: '2026-09-25T00:02:10Z', status: 'green', latency_ms: 1000 },
        ],
      },
    } })
    const bars = wrapper.findAll('[role="img"]')
    expect(bars).toHaveLength(2)
    expect(bars[0].classes()).toContain('bg-amber-400')
    expect(bars[1].classes()).toContain('bg-emerald-500')
    expect(wrapper.get('.grid').attributes('style')).toContain('repeat(2, minmax(0, 1fr))')
    expect(wrapper.text()).toContain('channelMonitorV3.intelligence.window')
  })
  it('compacts gaps chronologically without inventing or duplicating samples', () => {
    const uptime: Uptime = {
      model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30,
      status: 'green', window_start: '2026-09-25T00:00:00Z', bucket_seconds: 60, window_points: 60,
      points: [
        { checked_at: '2026-09-25T00:59:10Z', status: 'green', latency_ms: 3000 },
        { checked_at: '2026-09-25T00:02:10Z', status: 'yellow', latency_ms: 2000 },
        { checked_at: '2026-09-25T00:04:10Z', status: 'unknown', latency_ms: 0 },
        { checked_at: '2026-09-24T23:59:10Z', status: 'red', latency_ms: 1000 },
        { checked_at: '2026-09-25T01:00:10Z', status: 'red', latency_ms: 1000 },
      ],
    }
    const wrapper = mount(IntelligenceUptime, { props: { uptime } })
    const bars = wrapper.findAll('[role="img"]')
    expect(bars).toHaveLength(2)
    expect(bars[0].classes()).toContain('bg-amber-400')
    expect(bars[1].classes()).toContain('bg-emerald-500')
    expect(bars.every(bar => !bar.classes().includes('bg-gray-200'))).toBe(true)
    expect(wrapper.get('.grid').attributes('style')).toContain('repeat(2, minmax(0, 1fr))')
  })
  it('does not present errors as answer judgments', () => {
    const wrapper = mount(IntelligenceUptime, { props: { uptime: {
      model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30,
      status: 'red', points: [{ checked_at: '2026-09-25T00:00:00Z', status: 'red', latency_ms: 55000 }],
    } } })
    expect(wrapper.findAll('[role="img"]')).toHaveLength(0)
    expect(wrapper.get('[role="status"]').text()).toContain('unknown')
    expect(wrapper.get('[role="status"] i').classes()).not.toContain('bg-red-500')
  })
  it('keeps a previous green bar but not a green current state after a failed probe', () => {
    const wrapper = mount(IntelligenceUptime, { props: { uptime: {
      model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30,
      status: 'red', points: [{ checked_at: '2026-09-25T00:59:00Z', status: 'green', latency_ms: 500 }],
    } } })
    expect(wrapper.findAll('[role="img"]')).toHaveLength(1)
    expect(wrapper.get('[role="status"]').text()).toContain('unknown')
  })
  it('does not present an old successful sample as current health', () => {
    const wrapper = mount(IntelligenceUptime, { props: { uptime: {
      model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30,
      status: 'red', points: [{ checked_at: '2026-09-25T00:30:00Z', status: 'green', latency_ms: 500 }],
    } } })
    expect(wrapper.findAll('[role="img"]')).toHaveLength(1)
    expect(wrapper.get('[role="status"]').text()).toContain('unknown')
  })
  it('expands as actual results arrive and removes expired samples on refresh', async () => {
    const uptime: Uptime = {
      model: 'gpt-6-astra', reasoning_effort: 'medium', interval_seconds: 60, timeout_seconds: 30,
      status: 'unknown', window_start: '2026-09-25T00:00:00Z', bucket_seconds: 60, window_points: 60, points: [],
    }
    const wrapper = mount(IntelligenceUptime, { props: { uptime } })
    expect(wrapper.find('.grid').exists()).toBe(false)
    await wrapper.setProps({ uptime: { ...uptime, status: 'green', points: [
      { checked_at: '2026-09-25T00:00:10Z', status: 'green', latency_ms: 1000 },
    ] } })
    expect(wrapper.findAll('[role="img"]')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('channelMonitorV3.intelligence.noData')
    await wrapper.setProps({ uptime: { ...wrapper.props('uptime'), window_start: '2026-09-25T00:01:00Z' } })
    expect(wrapper.find('.grid').exists()).toBe(false)
    expect(wrapper.text()).toContain('channelMonitorV3.intelligence.noData')
  })
})
