<template>
  <span class="mt-1 block min-w-0 text-left text-xs leading-5 text-gray-500 dark:text-gray-400">
    <span v-if="message">{{ message }}</span>
    <template v-else>
      <span
        v-for="row in timelines"
        :key="row.platform"
        class="mb-1 block min-w-0 last:mb-0"
      >
        <span class="flex flex-wrap items-baseline gap-x-3">
          <span v-if="rows.length > 1" class="font-medium text-gray-700 dark:text-gray-200">
            {{ providerLabel(row.platform) }}
          </span>
          <template v-if="row.latest">
            <span class="whitespace-nowrap">
              {{ t('keys.groupHealth.availability') }}
              <strong class="font-semibold tabular-nums" :class="availabilityColor(row.latest)">
                {{ percent(row.latest.availability) }}
              </strong>
            </span>
            <span class="whitespace-nowrap">
              {{ t('keys.groupHealth.cacheRate') }}
              <strong class="font-semibold tabular-nums text-gray-800 dark:text-gray-100">
                {{ percent(row.latest.cacheRate) }}
              </strong>
            </span>
            <span
              v-if="row.latest.health === 'unknown'"
              class="whitespace-nowrap text-gray-600 dark:text-gray-300"
            >{{ t('keys.groupHealth.lowSample') }}</span>
            <span class="whitespace-nowrap tabular-nums">
              {{ t('keys.groupHealth.pointTime', { time: formatTime(row.latest.time) }) }}
            </span>
          </template>
          <span v-else>{{ t('keys.groupHealth.noData') }}</span>
        </span>
        <span
          role="img"
          :aria-label="t('keys.groupHealth.history')"
          :title="t('keys.groupHealth.history')"
          class="mt-1 flex h-2 w-full items-end gap-0.5"
        >
          <span
            v-for="point in row.slots"
            :key="point.time"
            class="min-w-0 flex-1 rounded-sm"
            :class="barClass(point)"
            :title="pointTitle(point)"
          />
        </span>
      </span>
      <span v-if="lagMinutes >= 5" class="block text-amber-700 dark:text-amber-300">
        {{ t('keys.groupHealth.lag', { n: lagMinutes }) }}
      </span>
    </template>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MonitorCoverage, MonitorMatrixRow } from '@/api/channelMonitorV2'
import { passiveTimelineWindow } from '@/components/user/monitor/passiveTimeline'
import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import { availabilityBarClass, availabilityTextClass, formatMonitorPercent } from '@/features/channel-monitor-v2/monitorFormat'
import { keyGroupHealthTimeline } from './keyGroupHealth'

const props = defineProps<{
  rows: MonitorMatrixRow[]
  coverage?: MonitorCoverage
  receivedAt: number
  state: 'loading' | 'ready' | 'error'
}>()

const { t, locale } = useI18n()
const { providerLabel } = useChannelMonitorFormat()
const window = computed(() => passiveTimelineWindow('90m', props.coverage, props.receivedAt))
const message = computed(() => {
  if (props.state === 'loading') return t('keys.groupHealth.loading')
  if (props.state === 'error') return t('keys.groupHealth.error')
  if (!props.rows.length) return t('keys.groupHealth.noData')
  if (!window.value.trailing?.compatible || props.coverage?.bucket_seconds !== window.value.step / 1000) {
    return t('keys.groupHealth.unavailable')
  }
  if (!window.value.trailing.ready) return t('keys.groupHealth.awaiting')
  return ''
})
const timelines = computed(() => props.rows.map(row => keyGroupHealthTimeline(row, window.value)))
const lagMinutes = computed(() => Math.floor((props.coverage?.aggregation_lag_seconds ?? 0) / 60))
const clock = computed(() => new Intl.DateTimeFormat(locale.value, { hour: '2-digit', minute: '2-digit' }))
const dateClock = computed(() => new Intl.DateTimeFormat(locale.value, {
  month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
}))
function formatTime(time: number) {
  const formatter = new Date(time).toDateString() === new Date(props.receivedAt).toDateString() ? clock : dateClock
  return formatter.value.format(time)
}

type Point = ReturnType<typeof keyGroupHealthTimeline>['slots'][number]

function percent(value: number | null) {
  return value === null ? '—' : formatMonitorPercent(value, locale.value)
}

function availabilityColor(point: Point) {
  return availabilityTextClass(point.availability === null || point.health === 'unknown' ? null : point.availability * 100)
}

const heights = { healthy: 'h-full', warning: 'h-1.5', critical: 'h-1' }
function barClass(point: Point) {
  if (point.health === null) return 'h-px bg-gray-300 dark:bg-dark-600'
  if (point.health === 'unknown') return 'h-1 bg-gray-400 dark:bg-gray-500'
  return [heights[point.health], availabilityBarClass(point.availability === null ? null : point.availability * 100)]
}

function pointTitle(point: Point) {
  const time = formatTime(point.time)
  if (!point.covered) return t('channelMonitorV3.pointUncovered', { time })
  if (!point.health) return t('channelMonitorV3.timelineNoData', { time })
  const metrics = t('channelMonitorV3.summary', { success: percent(point.availability), cache: percent(point.cacheRate) })
  return point.health === 'unknown' ? `${time} · ${t('keys.groupHealth.lowSample')} · ${metrics}` : `${time} · ${metrics}`
}
</script>
