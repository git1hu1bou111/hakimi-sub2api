import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { HealthState, MonitorCoverage, MonitorMatrixBucket, MonitorMatrixRow } from '@/api/channelMonitorV2'
import KeyGroupHealth from '../KeyGroupHealth.vue'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    locale: { value: 'en-GB' },
    t: (key: string, params: Record<string, unknown> = {}) => [key, ...Object.values(params)].join(' '),
  }),
}))

const start = Date.parse('2026-11-25T00:01:00Z')
const minute = 60_000
const iso = (delta: number) => new Date(start + delta * minute).toISOString()
const coverage: MonitorCoverage = {
  requested_start: iso(0), requested_end: iso(90), coverage_start: iso(5), data_through: iso(88),
  computed_at: iso(89), aggregation_lag_seconds: 120, coverage_complete: false, bucket_seconds: 300,
}
function bucket(offset: number, errorRate = 0.01, health: HealthState = 'healthy'): MonitorMatrixBucket {
  const latency = { p50_ms: null, p95_ms: null, avg_ms: null, sample_count: 0 }
  return {
    bucket_start: iso(offset),
    metrics: {
      request_count: 0, success_requests: 0, error_requests: 0, token_count: 0, rpm: 0, tpm: 0,
      cache_rate: 0.943, cache_rate_warm: 0, error_rate: errorRate,
      cache_rate_numerator: 0, cache_rate_denominator: 0, ttft: latency, duration: latency,
    },
    health: { overall: health, error_rate: health, ttft: health, minimum_sample: 50 },
  }
}
function row(buckets: MonitorMatrixBucket[], platform = 'openai'): MonitorMatrixRow {
  const aggregate = bucket(0, 0.5)
  return { platform, group_id: 7, cache_rate_display_cap: 90, buckets, health: aggregate.health, metrics: aggregate.metrics }
}
function render(rows: MonitorMatrixRow[], extra: Partial<InstanceType<typeof KeyGroupHealth>['$props']> = {}) {
  return mount(KeyGroupHealth, { props: { rows, coverage, receivedAt: start + 90 * minute, state: 'ready', ...extra } })
}

describe('KeyGroupHealth', () => {
  it('uses the latest covered real point despite redacted counts and keeps missing intervals empty', () => {
    const wrapper = render([row([bucket(90), bucket(81), bucket(80, 0.02), bucket(0), bucket(-5), bucket(5)])])
    expect(wrapper.text()).toContain('98.0%')
    expect(wrapper.text()).toContain('88.1%')
    const bars = wrapper.findAll('[role="img"] > span')
    expect(bars).toHaveLength(18)
    expect(bars[0].attributes('title')).toContain('channelMonitorV3.pointUncovered')
    expect(bars[1].classes()).toContain('h-full')
    expect(bars[16].classes()).toContain('h-full')
    expect(bars[17].attributes('title')).toContain('channelMonitorV3.timelineNoData')
    expect(wrapper.findAll('[role="img"] > .h-full')).toHaveLength(2)
    expect(wrapper.find('button, [tabindex]').exists()).toBe(false)
  })

  it('keeps delayed aggregation, low-sample measurements and empty data distinguishable', async () => {
    const wrapper = render([row([bucket(85, 1, 'unknown')])], { coverage: { ...coverage, aggregation_lag_seconds: 360 } })
    expect(wrapper.text()).toContain('keys.groupHealth.lag 6')
    expect(wrapper.text()).toContain('keys.groupHealth.lowSample')
    expect(wrapper.findAll('[role="img"] > span')[17].classes()).toContain('bg-gray-400')
    await wrapper.setProps({ rows: [row([])] })
    expect(wrapper.text()).toContain('keys.groupHealth.noData')
    expect(wrapper.text()).not.toContain('%')
    expect(wrapper.text()).not.toContain('keys.groupHealth.lowSample')
    await wrapper.setProps({ state: 'error' })
    expect(wrapper.text()).toBe('keys.groupHealth.error')
    await wrapper.setProps({ state: 'loading' })
    expect(wrapper.text()).toBe('keys.groupHealth.loading')
  })

  it('never displays an incompatible grid or uses an uncovered bucket while aggregation is pending', async () => {
    const wrapper = render([row([bucket(5)])], { coverage: { ...coverage, requested_start: iso(-1), requested_end: iso(89) } })
    expect(wrapper.text()).toBe('keys.groupHealth.unavailable')
    await wrapper.setProps({ coverage: { ...coverage, computed_at: '0001-01-01T00:00:00Z' } })
    expect(wrapper.text()).toBe('keys.groupHealth.awaiting')
    await wrapper.setProps({ coverage: { ...coverage, data_through: iso(5) } })
    expect(wrapper.text()).toContain('keys.groupHealth.noData')
    expect(wrapper.text()).not.toContain('%')
  })

  it('shows each platform’s measurements alongside its own history', () => {
    const warm = bucket(85, 0.2)
    warm.metrics.cache_rate_warm = 0.70
    const wrapper = render([row([bucket(75, 0.01)]), row([warm], 'gemini')])
    expect(wrapper.findAll('[role="img"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('monitorCommon.providers.openai')
    expect(wrapper.text()).toContain('monitorCommon.providers.gemini')
    expect(wrapper.text()).toContain('99.0%')
    expect(wrapper.text()).toContain('80.0%')
    expect(wrapper.text()).toContain('70.0%')
    expect(wrapper.text().match(/keys\.groupHealth\.pointTime/g)).toHaveLength(2)
  })
})
