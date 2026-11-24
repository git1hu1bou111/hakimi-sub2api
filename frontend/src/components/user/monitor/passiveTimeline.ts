import type { MonitorCoverage, MonitorRange } from '@/api/channelMonitorV2'

const ranges = {
  '90m': { length: 18, seconds: 300 },
  '24h': { length: 24, seconds: 3600 },
  '7d': { length: 14, seconds: 43200 },
  '30d': { length: 30, seconds: 86400 },
}

export function passiveTimelineWindow(range: MonitorRange, coverage: MonitorCoverage | undefined, receivedAt: number) {
  const { length, seconds } = ranges[range]
  const step = seconds * 1000
  const requestedStart = Date.parse(coverage?.requested_start ?? '')
  const requestedEnd = Date.parse(coverage?.requested_end ?? '')
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

export type PassiveTimelineWindow = ReturnType<typeof passiveTimelineWindow>
