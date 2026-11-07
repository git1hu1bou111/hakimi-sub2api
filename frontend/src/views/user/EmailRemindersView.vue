<template>
  <AppLayout>
    <div class="mx-auto max-w-[950px] space-y-6" data-testid="email-reminders-shell">
      <section class="card overflow-hidden">
        <div class="flex items-start gap-4 p-5 sm:p-6">
          <div class="flex h-12 w-12 shrink-0 items-center justify-center rounded-2xl bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
            <Icon name="emailReminder" size="xl" aria-hidden="true" />
          </div>
          <div class="min-w-0">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('emailReminders.introTitle') }}
            </h2>
            <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">
              {{ t('emailReminders.introDescription') }}
            </p>
          </div>
        </div>
        <div
          v-if="!loading && !loadFailed && balanceLowNotifyEnabled && user"
          class="border-t border-gray-100 bg-gray-50/70 px-5 py-4 dark:border-dark-700 dark:bg-dark-900/30 sm:px-6"
          aria-live="polite"
          data-testid="reminder-status"
        >
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span class="text-gray-500 dark:text-gray-400">{{ t('emailReminders.statusLabel') }}</span>
            <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium" :class="statusClasses">
              <span class="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true"></span>
              {{ t(`emailReminders.${statusTitleKey}`) }}
            </span>
          </div>
          <p class="mt-2 text-sm leading-6 text-gray-600 dark:text-gray-300">
            {{ t(`emailReminders.${statusHintKey}`) }}
          </p>
        </div>
      </section>

      <div v-if="loading" class="flex items-center justify-center gap-3 py-12 text-sm text-gray-500 dark:text-gray-400" role="status">
        <Icon name="refresh" class="animate-spin text-primary-500" aria-hidden="true" />
        <span>{{ t('common.loading') }}</span>
      </div>
      <div v-else-if="loadFailed" class="card p-6" role="alert">
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('emailReminders.loadFailed') }}</p>
        <button class="btn btn-secondary mt-4" @click="loadSettings">
          {{ t('emailReminders.retry') }}
        </button>
      </div>
      <ProfileBalanceNotifyCard
        v-else-if="user && balanceLowNotifyEnabled"
        :key="user.id"
        :enabled="user.balance_notify_enabled ?? true"
        :threshold="user.balance_notify_threshold"
        :extra-emails="user.balance_notify_extra_emails ?? []"
        :system-default-threshold="systemDefaultThreshold"
        :user-email="user.email"
      />
      <div v-else class="card p-6" data-testid="reminders-unavailable">
        <h2 class="font-medium text-gray-900 dark:text-white">{{ t('emailReminders.unavailableTitle') }}</h2>
        <p class="mt-2 text-sm leading-6 text-gray-500 dark:text-gray-400">
          {{ t('emailReminders.unavailableDescription') }}
        </p>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import AppLayout from '@/components/layout/AppLayout.vue'
import ProfileBalanceNotifyCard from '@/components/user/profile/ProfileBalanceNotifyCard.vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { PublicSettings } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const user = computed(() => authStore.user)
const settings = ref<PublicSettings | null>(null)
const loading = ref(true)
const loadFailed = ref(false)
const balanceLowNotifyEnabled = computed(() => settings.value?.balance_low_notify_enabled === true)
const systemDefaultThreshold = computed(() => settings.value?.balance_low_notify_threshold ?? 0)
const reminderStatus = computed(() => {
  if (user.value?.balance_notify_enabled === false) return 'paused'
  const hasRecipient = user.value?.balance_notify_extra_emails?.some(
    (entry) => entry.verified && !entry.disabled && entry.email.trim()
  )
  if (!hasRecipient) return 'needsEmail'
  if ((user.value?.balance_notify_threshold ?? systemDefaultThreshold.value) <= 0) return 'needsThreshold'
  return 'ready'
})
const statusTitleKey = computed(() => ({
  paused: 'statusPaused', needsEmail: 'statusNeedsEmail', needsThreshold: 'statusNeedsThreshold', ready: 'statusReady'
}[reminderStatus.value]))
const statusHintKey = computed(() => `${statusTitleKey.value}Hint`)
const statusClasses = computed(() => ({
  paused: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300',
  needsEmail: 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300',
  needsThreshold: 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300',
  ready: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
}[reminderStatus.value]))

async function loadSettings() {
  loading.value = true
  loadFailed.value = false
  try {
    const [, publicSettings] = await Promise.all([
      authStore.refreshUser(),
      appStore.fetchPublicSettings()
    ])
    if (!publicSettings || !user.value) throw new Error('Missing reminder settings')
    settings.value = publicSettings
  } catch {
    loadFailed.value = true
  } finally {
    loading.value = false
  }
}

onMounted(loadSettings)
</script>
