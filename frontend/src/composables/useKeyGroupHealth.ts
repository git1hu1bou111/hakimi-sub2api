import { computed, ref, shallowRef, watch, type Ref } from 'vue'
import { useDocumentVisibility, useEventListener } from '@vueuse/core'
import { getAuthToken } from '@/api/auth'
import { getMatrix, type MonitorCoverage, type MonitorMatrixRow } from '@/api/channelMonitorV2'
import { useAuthStore } from '@/stores/auth'

const refreshMs = 60_000

interface GroupHealthSnapshot {
  rows: Map<number, MonitorMatrixRow[]>
  coverage: MonitorCoverage
  receivedAt: number
}

// The HTTP client reads credentials from localStorage, which can change in
// another tab before this tab's Pinia user has caught up.
function storedViewerId(): number | undefined {
  if (!getAuthToken()) return undefined
  try {
    return JSON.parse(localStorage.getItem('auth_user') ?? 'null')?.id
  } catch {
    return undefined
  }
}

export function useKeyGroupHealth(active: Ref<boolean>) {
  const auth = useAuthStore()
  const visibility = useDocumentVisibility()
  const storedId = ref(storedViewerId())
  function syncStoredId() { storedId.value = storedViewerId() }
  watch([() => auth.token, () => auth.user?.id], syncStoredId)
  useEventListener(window, 'storage', (event) => {
    if (!event.key || event.key === 'auth_user' || event.key === 'auth_token') syncStoredId()
  })

  const viewer = computed(() => auth.isAuthenticated && auth.user?.id === storedId.value ? storedId.value : undefined)
  const enabled = computed(() => active.value && visibility.value === 'visible')
  // Owned by the keys view, so opening multiple group options shares one
  // snapshot and navigating away discards it.
  const snapshot = shallowRef<GroupHealthSnapshot>()
  const state = ref<'loading' | 'ready' | 'error'>('loading')

  watch([viewer, enabled], ([userId, isEnabled], [previousUserId], onCleanup) => {
    if (userId !== previousUserId) {
      snapshot.value = undefined
      state.value = 'loading'
    }
    if (!isEnabled) return
    syncStoredId()
    if (!userId || userId !== viewer.value) return

    const request = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    onCleanup(() => {
      request.abort()
      clearTimeout(timer)
    })

    async function refresh() {
      syncStoredId()
      if (request.signal.aborted) return
      if (!snapshot.value) state.value = 'loading'
      try {
        const result = await getMatrix(
          { range: '90m', platforms: [], groupIds: [], models: [] },
          'platform_group', false, request.signal, true,
        )
        syncStoredId()
        if (request.signal.aborted) return
        const rows = new Map<number, MonitorMatrixRow[]>()
        for (const row of result.items) {
          if (!row.group_id || row.group_id <= 0) continue
          const group = rows.get(row.group_id)
          if (group) group.push(row)
          else rows.set(row.group_id, [row])
        }
        snapshot.value = { rows, coverage: result.coverage, receivedAt: Date.now() }
        state.value = 'ready'
      } catch {
        if (request.signal.aborted) return
        snapshot.value = undefined
        state.value = 'error'
      }
      timer = setTimeout(refresh, refreshMs)
    }

    const remaining = snapshot.value ? snapshot.value.receivedAt + refreshMs - Date.now() : 0
    if (remaining > 0) timer = setTimeout(refresh, remaining)
    else void refresh()
  }, { immediate: true, flush: 'sync' })

  function rowsForGroup(id: number, platform?: string): MonitorMatrixRow[] {
    const rows = snapshot.value?.rows.get(id) ?? []
    return platform ? rows.filter(row => row.platform === platform) : rows
  }

  return {
    rowsForGroup,
    coverage: computed(() => snapshot.value?.coverage),
    receivedAt: computed(() => snapshot.value?.receivedAt ?? 0),
    state,
  }
}
