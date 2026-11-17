<template>
  <div class="space-y-0.5 whitespace-nowrap text-xs">
    <span v-if="!wallet" class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.upstreamWallet.pending') }}</span>
    <span v-else-if="wallet.status === 'unsupported'" class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.upstreamWallet.unsupported') }}</span>
    <template v-else>
      <div
        v-for="balance in balances"
        :key="balance.currency"
        class="font-medium tabular-nums"
        :class="balance.amount <= 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-800 dark:text-gray-200'"
      >
        {{ numberFormat.format(balance.amount) }} {{ balance.currency }}
      </div>
      <div v-if="wallet.status === 'error' || !balances.length" class="text-amber-700 dark:text-amber-400">
        {{ t('admin.accounts.upstreamWallet.error') }}
      </div>
      <div v-if="balances.length && wallet.updated_at" class="text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.upstreamWallet.updated', { time: formatDateTimeToMinute(wallet.updated_at) }) }}
      </div>
      <div v-if="wallet.status === 'error' && wallet.checked_at" class="text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.upstreamWallet.checked', { time: formatDateTimeToMinute(wallet.checked_at) }) }}
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UpstreamWalletSnapshot } from '@/types'
import { formatDateTimeToMinute } from '@/utils/format'

const props = defineProps<{ wallet?: UpstreamWalletSnapshot | null }>()
const { t, locale } = useI18n()
const numberFormat = computed(() => new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }))
const balances = computed(() => Array.isArray(props.wallet?.balances)
  ? props.wallet.balances.filter(item => item && Number.isFinite(item.amount) && typeof item.currency === 'string')
  : [])
</script>
