<template>
  <div class="space-y-0.5 text-xs">
    <template v-if="loading && !metrics">
      <div class="h-3 w-14 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-16 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-12 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
    </template>
    <span v-else-if="idle" class="text-gray-400">{{ t('admin.accounts.liveMetrics.idle') }}</span>
    <template v-else>
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.liveMetrics.cache') }}</span>
        <span class="font-medium" :class="cacheClass">{{ cacheLabel }}</span>
      </div>
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.liveMetrics.ttft') }}</span>
        <span class="font-medium" :class="ttftClass">{{ ttftLabel }}</span>
      </div>
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.liveMetrics.errors') }}</span>
        <span class="font-medium" :class="errClass">{{ errLabel }}</span>
        <span v-if="metrics && metrics.error_requests_1h > 0" class="text-gray-400"
          >({{ metrics.error_requests_1h }})</span
        >
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountLiveMetrics } from '@/api/admin/accounts'

const props = defineProps<{ metrics?: AccountLiveMetrics | null; loading?: boolean }>()
const { t } = useI18n()

const idle = computed(
  () => !props.metrics || (props.metrics.requests_10m === 0 && props.metrics.requests_1h === 0)
)
const cacheValue = computed(() => props.metrics?.cache_hit_pct ?? 0)
const ttftSeconds = computed(() => (props.metrics?.ttft_p50_ms ?? 0) / 1000)
const errValue = computed(() => props.metrics?.error_rate_pct ?? 0)

const cacheLabel = computed(() =>
  props.metrics && props.metrics.prompt_tokens_10m > 0 ? `${cacheValue.value.toFixed(1)}%` : '—'
)
const ttftLabel = computed(() =>
  props.metrics && props.metrics.ttft_samples > 0 ? `${ttftSeconds.value.toFixed(1)}s` : '—'
)
const errLabel = computed(() =>
  props.metrics && props.metrics.requests_1h > 0
    ? `${errValue.value < 10 ? errValue.value.toFixed(2) : errValue.value.toFixed(1)}%`
    : '—'
)

const okClass = 'text-emerald-600 dark:text-emerald-400'
const warnClass = 'text-amber-600 dark:text-amber-400'
const badClass = 'text-red-500 dark:text-red-400'

const cacheClass = computed(() => {
  if (!props.metrics || props.metrics.prompt_tokens_10m === 0) return 'text-gray-400'
  return cacheValue.value >= 80 ? okClass : cacheValue.value >= 50 ? warnClass : badClass
})
const ttftClass = computed(() => {
  if (!props.metrics || props.metrics.ttft_samples === 0) return 'text-gray-400'
  return ttftSeconds.value <= 5 ? okClass : ttftSeconds.value <= 15 ? warnClass : badClass
})
const errClass = computed(() => {
  if (!props.metrics || props.metrics.requests_1h === 0) return 'text-gray-400'
  return errValue.value <= 1 ? okClass : errValue.value <= 5 ? warnClass : badClass
})
</script>
