import type { MonitorCoverage, MonitorRange } from '@/api/channelMonitorV2'

const ranges = {
  '90m': { length: 18, seconds: 300 },
  '24h': { length: 24, seconds: 3600 },
  '7d': { length: 14, seconds: 43200 },
  '30d': { length: 30, seconds: 86400 },
}

export const minuteMs = 60_000

interface TrailingCoverage {
  compatible: boolean
  ready: boolean
  from: number
  through: number
}

export interface PassiveTimelineWindow {
  start: number
  step: number
  length: number
  trailing?: TrailingCoverage
}

export function passiveTimelineWindow(range: MonitorRange, coverage: MonitorCoverage | undefined, receivedAt: number): PassiveTimelineWindow {
  const { length, seconds } = ranges[range]
  const step = seconds * 1000
  const requestedStart = Date.parse(coverage?.requested_start ?? '')
  const requestedEnd = Date.parse(coverage?.requested_end ?? '')
  if (range === '90m') {
    // Trailing windows start one minute after a five-minute mark. Legacy
    // matrices use different minute membership and cannot populate this grid.
    const anchoredStart = requestedStart % step === minuteMs ? requestedStart : requestedEnd - length * step
    const compatible = anchoredStart > 0 && anchoredStart % step === minuteMs
    const through = Date.parse(coverage?.data_through ?? '')
    const from = Date.parse(coverage?.coverage_start ?? '')
    const ready = Date.parse(coverage?.computed_at ?? '') > 0 && through > 0 && from > 0
    return {
      start: compatible ? anchoredStart : Math.floor(receivedAt / step) * step + minuteMs - length * step,
      step, length, trailing: { compatible, ready, from, through },
    }
  }
  // Older responses can omit either bound. Reconstruct the server's UTC grid
  // at receipt time when neither bound is available.
  const end = Number.isFinite(requestedEnd)
    ? Math.ceil(requestedEnd / step) * step
    : (Math.floor(receivedAt / step) + 1) * step
  const start = Number.isFinite(requestedStart)
    ? Math.ceil(requestedStart / step) * step
    : end - length * step
  return { start, step, length }
}
