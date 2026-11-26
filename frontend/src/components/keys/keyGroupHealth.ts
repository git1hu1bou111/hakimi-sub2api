import type { MonitorMatrixRow } from '@/api/channelMonitorV2'
import { minuteMs, type PassiveTimelineWindow } from '@/components/user/monitor/passiveTimeline'
import { cacheRateForDisplay } from '@/features/channel-monitor-v2/monitorFormat'

function isRate(value: number | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1
}

/** Project one platform's real buckets onto the same five-minute grid as the status page. */
export function keyGroupHealthTimeline(row: MonitorMatrixRow, window: PassiveTimelineWindow) {
  const { start, step, length, trailing } = window
  const buckets = new Map(row.buckets.map(bucket => [Date.parse(bucket.bucket_start), bucket]))
  const slots = Array.from({ length }, (_, index) => {
    const slotStart = start + index * step
    const covered = !!trailing?.compatible && trailing.ready &&
      Math.max(slotStart, trailing.from) < Math.min(slotStart + step, trailing.through)
    const bucket = covered ? buckets.get(slotStart) : undefined
    const metrics = bucket?.metrics
    // Public matrices redact warm cache rates to zero. Use the same fallback as the V3 card.
    const cache = metrics?.cache_rate_warm || metrics?.cache_rate
    const availability = isRate(metrics?.error_rate) ? 1 - metrics.error_rate : null
    const cacheRate = isRate(cache) ? cacheRateForDisplay(cache, row.cache_rate_display_cap) : null
    const hasMetrics = availability !== null || cacheRate !== null
    return {
      time: slotStart + step - minuteMs,
      covered,
      health: hasMetrics ? bucket?.health?.overall ?? 'unknown' : null,
      availability,
      cacheRate,
    }
  })
  // Empty grid cells carry no measurement. Never fill them from the full-range aggregate.
  const latest = slots.filter(slot => slot.health !== null).at(-1)
  return { platform: row.platform, slots, latest }
}
