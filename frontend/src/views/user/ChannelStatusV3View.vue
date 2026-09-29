<template>
  <AppLayout>
    <div class="space-y-5 pb-12">
      <section class="glass-card overflow-hidden p-0">
        <header class="flex flex-wrap items-start justify-between gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:px-6">
          <div class="min-w-0">
            <h1 class="page-title flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
              <span class="grid h-8 w-8 place-items-center rounded-xl bg-primary-50 text-primary-500 dark:bg-primary-900/30 dark:text-primary-300"><Icon name="chart" size="sm" /></span>
              {{ t('channelMonitorV3.title') }}
            </h1>
            <div class="mt-1.5 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span class="h-2 w-2 rounded-full" :class="refreshing ? 'bg-gray-400' : 'bg-emerald-500'" />
              <span>{{ snapshot ? t('channelMonitorV3.updatedTo', { time: formatTime(snapshot.coverage.data_through) }) : t('common.loading') }}</span>
              <span v-if="snapshot && !snapshot.coverage.coverage_complete" class="badge badge-warning">{{ t('channelMonitorV3.partialCoverage') }}</span>
            </div>
          </div>
          <button class="btn btn-secondary btn-icon h-8 w-8 rounded-lg" type="button" :disabled="loading || refreshing" :title="t('common.refresh')" @click="reload(false)"><Icon name="refresh" size="sm" :class="refreshing ? 'animate-spin' : ''" /></button>
        </header>
        <div class="flex flex-wrap items-center gap-2 px-4 py-3 sm:px-5">
          <button v-for="option in ranges" :key="option.value" type="button" class="tab !px-2.5 !py-1 text-xs" :class="filter.range === option.value ? 'tab-active' : ''" @click="setRange(option.value)">{{ option.label }}</button>
          <span class="mx-1 hidden h-5 w-px bg-gray-200 dark:bg-dark-700 sm:block" />
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.description') }}</span>
          <span v-if="snapshot" class="ml-auto text-xs font-medium tabular-nums text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.summary', { success: formatPercent(1 - (latestSnapshotMetrics?.error_rate ?? 0)), cache: formatPercent(latestSnapshotMetrics?.cache_rate ?? 0) }) }}</span>
        </div>
      </section>

      <div v-if="loading && rows.length === 0" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        <div v-for="i in 8" :key="i" class="h-72 animate-pulse rounded-[24px] bg-white/60 dark:bg-dark-800" />
      </div>
      <EmptyState v-else-if="rows.length === 0" :title="t('channelMonitorV3.emptyTitle')" :description="t('channelMonitorV3.emptyDescription')" />
      <div v-else class="space-y-8">
        <section
          v-for="(section, index) in platformSections"
          :key="section.platform"
          :aria-labelledby="`monitor-platform-${index}`"
          :data-platform="section.platform"
          class="space-y-4"
        >
          <h2 :id="`monitor-platform-${index}`" class="flex items-center gap-2.5 text-sm font-semibold text-gray-800 dark:text-gray-100">
            <span class="grid h-7 w-7 shrink-0 place-items-center rounded-full" :class="providerGradient(section.platform)" aria-hidden="true">
              <ProviderIcon :provider="section.platform" :size="16" />
            </span>
            <span>{{ providerLabel(section.platform) }}</span>
            <span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium tabular-nums text-gray-500 dark:bg-dark-700 dark:text-gray-400" :aria-label="t('channelMonitorV3.groupCount', { count: section.rows.length })">
              {{ section.rows.length }}
            </span>
          </h2>
          <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            <ChannelMonitorV3Card
              v-for="row in section.rows"
              :key="row.group_id"
              :row="row"
              :user-rate-multiplier="getUserRateMultiplier(row.group_id)"
              :countdown-seconds="countdownSeconds"
              :timeline-length="timelineLength"
              :coverage="matrix?.coverage"
            />
          </div>
        </section>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import * as api from '@/api/channelMonitorV2'
import userGroupsAPI from '@/api/groups'
import type { MonitorFilter, MonitorMatrixResponse, MonitorMatrixRow, MonitorRange, MonitorSnapshot } from '@/api/channelMonitorV2'
import type { Group } from '@/types'
import ChannelMonitorV3Card from '@/components/user/monitor/ChannelMonitorV3Card.vue'
import ProviderIcon from '@/components/user/monitor/ProviderIcon.vue'
import { providerGradient, useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import { formatMonitorPercent } from '@/features/channel-monitor-v2/monitorFormat'

const { t, locale } = useI18n()
const { providerLabel } = useChannelMonitorFormat()
const appStore = useAppStore()
const ranges = computed(() => [
  { value: '90m' as MonitorRange, label: t('channelMonitorV3.ranges.90m') },
  { value: '24h' as MonitorRange, label: t('channelMonitorV3.ranges.24h') },
  { value: '7d' as MonitorRange, label: t('channelMonitorV3.ranges.7d') },
  { value: '30d' as MonitorRange, label: t('channelMonitorV3.ranges.30d') },
])
const filter = ref<MonitorFilter>({ range: '90m', platforms: [], groupIds: [], models: [] })
const snapshot = ref<MonitorSnapshot | null>(null)
const matrix = ref<MonitorMatrixResponse | null>(null)
const loading = ref(false)
const refreshing = ref(false)
const userGroupRates = ref<Record<number, number>>({})
const countdownSeconds = ref(0)
let controller: AbortController | null = null
let refreshTimer: number | null = null
let countdownTimer: number | null = null

// platform_group is intentionally used here: the backend scopes it to the
// monitor group_ids selected by the operator, so unrelated groups never appear.
const rows = computed(() => [...(matrix.value?.items ?? [])]
  .filter(row => row.group_id != null && row.group_id > 0)
  .sort((a, b) => (a.group_id ?? 0) - (b.group_id ?? 0)))
const platformOrder = ['openai', 'anthropic', 'grok', 'gemini']
const platformSections = computed(() => {
  const groups = new Map<string, MonitorMatrixRow[]>()
  for (const row of rows.value) {
    const group = groups.get(row.platform)
    if (group) group.push(row)
    else groups.set(row.platform, [row])
  }
  const rank = (platform: string) => {
    const index = platformOrder.indexOf(platform)
    return index < 0 ? platformOrder.length : index
  }
  // Preserve every returned platform, including ones added by future providers.
  return [...groups].map(([platform, rows]) => ({ platform, rows }))
    .sort((a, b) => rank(a.platform) - rank(b.platform) || a.platform.localeCompare(b.platform))
})
const timelineLength = computed(() => ({ '90m': 18, '24h': 24, '7d': 14, '30d': 30 })[filter.value.range])
const latestSnapshotMetrics = computed(() => {
  const trend = [...(snapshot.value?.trend ?? [])]
    .filter(point => point.bucket_start && point.metrics)
    .sort((a, b) => Date.parse(a.bucket_start) - Date.parse(b.bucket_start))
  return trend.at(-1)?.metrics ?? snapshot.value?.metrics
})

function formatPercent(value: number) { return formatMonitorPercent(value, locale.value || 'zh-CN') }
function formatTime(value?: string) { if (!value) return '-'; return new Intl.DateTimeFormat(locale.value || undefined, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value)) }
function setRange(value: MonitorRange) { filter.value = { ...filter.value, range: value } }

function getUserRateMultiplier(groupId?: number) {
  if (!groupId) return null
  const value = userGroupRates.value[groupId]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

async function loadUserGroupRates() {
  try {
    const [groups, customRates] = await Promise.all([
      userGroupsAPI.getAvailable(),
      userGroupsAPI.getUserGroupRates(),
    ])
    userGroupRates.value = Object.fromEntries(
      (groups as Group[]).map(group => [group.id, customRates[group.id] ?? group.rate_multiplier]),
    )
  } catch (error) {
    // Monitoring remains usable when the optional pricing endpoint is unavailable.
    console.warn('Failed to load channel monitor group rates', error)
  }
}

async function reload(silent = true) {
  controller?.abort()
  const request = new AbortController()
  controller = request
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    const [nextSnapshot, nextMatrix] = await Promise.all([
      api.getSnapshot(filter.value, false, request.signal),
      api.getMatrix(filter.value, 'platform_group', false, request.signal),
    ])
    if (request.signal.aborted || controller !== request) return
    snapshot.value = nextSnapshot
    matrix.value = nextMatrix
    // Active intelligence probes must not wait for the passive monitor's
    // optional five-minute refresh. Keep the faster bootstrap polling intact.
    scheduleRefresh(nextSnapshot.coverage.bootstrap?.active ? 10 : Math.min(60, nextSnapshot.config.refresh_interval_seconds))
  } catch (error) {
    const e = error as { name?: string; code?: string }
    if (e.name !== 'AbortError' && e.code !== 'ERR_CANCELED') appStore.showError(extractApiErrorMessage(error, t('channelMonitorV3.loadFailed')))
  } finally {
    if (controller === request) { loading.value = false; refreshing.value = false }
  }
}

function scheduleRefresh(seconds: number) {
  const interval = Math.max(10, seconds)
  if (refreshTimer) window.clearInterval(refreshTimer)
  if (countdownTimer) window.clearInterval(countdownTimer)
  countdownSeconds.value = interval
  countdownTimer = window.setInterval(() => {
    if (!document.hidden && countdownSeconds.value > 0) countdownSeconds.value -= 1
  }, 1000)
  refreshTimer = window.setInterval(() => {
    if (!loading.value && !refreshing.value && !document.hidden) void reload(true)
  }, interval * 1000)
}

watch(() => filter.value.range, () => void reload(false))
onMounted(() => { void loadUserGroupRates(); void reload(false) })
onBeforeUnmount(() => {
  controller?.abort()
  if (refreshTimer) window.clearInterval(refreshTimer)
  if (countdownTimer) window.clearInterval(countdownTimer)
})
</script>
