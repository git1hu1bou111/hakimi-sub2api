<template>
  <section :class="compact ? 'min-w-[160px] max-w-[240px]' : 'mt-4 border-t border-gray-200/70 pt-3 dark:border-dark-700/60'" :aria-label="t('channelMonitorV3.intelligence.title')">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <span class="text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('channelMonitorV3.intelligence.title') }}</span>
      <span class="inline-flex items-center gap-1.5 text-xs font-medium text-gray-600 dark:text-gray-300" role="status">
        <i class="h-2 w-2 rounded-full" :class="color(status)" aria-hidden="true" />
        {{ label(status) }}
      </span>
    </div>
    <div v-if="!compact" class="mt-1 text-[10px] text-gray-500 dark:text-gray-400">
      {{ uptime?.model ?? 'gpt-6-astra' }} · medium · {{ t('channelMonitorV3.intelligence.interval') }}
      <span v-if="uptime?.window_start"> · {{ t('channelMonitorV3.intelligence.window', { count: slotCount }) }}</span>
    </div>
    <div v-if="visibleSlots.length" class="mt-2 grid h-5" :class="visibleSlots.length > 30 ? 'gap-px' : 'gap-[3px]'" :style="{ gridTemplateColumns: `repeat(${visibleSlots.length}, minmax(0, 1fr))` }">
      <span v-for="point in visibleSlots" :key="point.checked_at"
        class="min-w-0 rounded-sm outline-offset-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
        :class="color(point.status)" tabindex="0" role="img"
        :title="tooltip(point)" :aria-label="tooltip(point)" />
    </div>
    <div v-else class="mt-2 flex h-5 items-center text-[10px] text-gray-400 dark:text-gray-500">
      {{ t('channelMonitorV3.intelligence.noData') }}
    </div>
    <div v-if="!compact" class="mt-1.5 flex justify-between gap-2 text-[10px] text-gray-500 dark:text-gray-400">
      <span>{{ t('channelMonitorV3.intelligence.groupLegend') }}</span>
      <time v-if="uptime?.checked_at" :datetime="uptime.checked_at">{{ time(uptime.checked_at) }}</time>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { IntelligenceUptime, IntelligencePoint, IntelligenceStatus, MonitorCoverage } from '@/api/channelMonitorV2'

const props = withDefaults(defineProps<{
  uptime?: IntelligenceUptime
  coverage?: MonitorCoverage
  length?: number
  compact?: boolean
}>(), { length: 60 })
const { t, locale } = useI18n()
const status = computed<IntelligenceStatus>(() => {
  const latest = [...visibleSlots.value].reverse()[0]
  return props.uptime?.status !== 'red' && latest && Date.now() - Date.parse(latest.checked_at) <= 3 * 60_000
    ? latest.status : 'unknown'
})
const slotCount = computed(() => props.uptime?.window_points ?? props.length)
const slots = computed(() => {
  const result: Array<IntelligencePoint | null> = Array.from({ length: slotCount.value }, () => null)
  const points = props.uptime?.points ?? []
  const start = Date.parse(props.uptime?.window_start ?? props.coverage?.requested_start ?? '')
  const width = (props.uptime?.bucket_seconds ?? props.coverage?.bucket_seconds ?? 0) * 1000
  if (Number.isFinite(start) && width > 0) {
    for (const point of points) {
      const index = Math.floor((Date.parse(point.checked_at) - start) / width)
      if (index >= 0 && index < result.length) result[index] = point
    }
  } else {
    points.slice(-slotCount.value).forEach((point, index, list) => { result[result.length - list.length + index] = point })
  }
  return result
})
// Errors and missing minutes are not answer judgments. Keep only real,
// conclusive samples and expand them to fill the row.
const visibleSlots = computed(() => slots.value.filter((point): point is IntelligencePoint =>
  point !== null && ['green', 'yellow'].includes(point.status),
))
function color(value: IntelligenceStatus) {
  return {
    green: 'bg-emerald-500 dark:bg-emerald-400',
    yellow: 'bg-amber-400 dark:bg-amber-400',
    red: 'bg-red-500 dark:bg-red-400',
    stale: 'bg-gray-400 dark:bg-gray-500',
    unknown: 'bg-gray-200 dark:bg-dark-600',
  }[value] ?? 'bg-gray-200 dark:bg-dark-600'
}
function label(value: IntelligenceStatus) {
  return t(`channelMonitorV3.intelligence.${!props.compact && value === 'yellow' ? 'groupYellow' : value}`)
}
function time(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '-' : new Intl.DateTimeFormat(locale.value || undefined, { hour: '2-digit', minute: '2-digit' }).format(date)
}
function tooltip(point: IntelligencePoint | null) {
  if (!point) return label('unknown')
  return `${new Date(point.checked_at).toLocaleString(locale.value || undefined)} · ${label(point.status)} · ${(point.latency_ms / 1000).toFixed(1)}s`
}
</script>
