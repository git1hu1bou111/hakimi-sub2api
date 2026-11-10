<template>
  <section class="space-y-5" aria-label="USDT 充值">
    <div class="overflow-hidden rounded-2xl border border-teal-200 bg-white dark:border-teal-900 dark:bg-dark-800">
      <div class="flex items-center gap-4 bg-teal-50 px-6 py-5 dark:bg-teal-950/40">
        <svg aria-hidden="true" viewBox="0 0 64 64" class="h-14 w-14 shrink-0 text-teal-600" fill="none">
          <rect x="5" y="12" width="50" height="40" rx="12" fill="currentColor" opacity=".12"/>
          <path d="M14 21h28M28 21v22M18 30c0 5 22 5 22 0" stroke="currentColor" stroke-width="4" stroke-linecap="round"/>
          <circle cx="49" cy="43" r="12" fill="currentColor"/>
          <path d="M49 38v10m-5-5h10" stroke="white" stroke-width="2.5" stroke-linecap="round"/>
        </svg>
        <div><h2 class="text-xl font-bold text-gray-900 dark:text-white">USDT 充值</h2>
          <p class="mt-1 text-sm text-teal-800 dark:text-teal-200">实时汇率折算 · 每笔额外赠送 1%</p></div>
      </div>
      <div class="space-y-6 p-6">
        <fieldset :disabled="busy">
          <legend class="mb-3 text-sm font-semibold">充值金额 <span class="font-normal text-gray-400">USDT</span></legend>
          <div class="grid grid-cols-3 gap-3 sm:grid-cols-6">
            <button v-for="value in [10, 50, 100, 200, 500, 1000]" :key="value" type="button"
              class="rounded-xl border px-3 py-3 font-semibold transition-colors"
              :class="Number(amount) === value ? 'border-teal-500 bg-teal-50 text-teal-700 dark:bg-teal-950' : 'border-gray-200 dark:border-dark-600'"
              @click="amount = String(value)">{{ value }} <span class="block text-xs font-normal text-teal-600">+1%</span></button>
          </div>
          <label class="mt-4 block text-sm text-gray-500" for="usdt-amount">自定义金额（USDT）</label>
          <p v-if="methods[selected]?.single_min > 0" class="mt-1 text-xs text-gray-400">最低 {{ methods[selected].single_min }} USDT<span v-if="methods[selected]?.single_max > 0">，最高 {{ methods[selected].single_max }} USDT</span></p>
          <input id="usdt-amount" v-model="amount" class="input mt-2 w-full" inputmode="decimal" placeholder="例如 10" autocomplete="off" />
        </fieldset>
        <fieldset :disabled="busy">
          <legend class="mb-3 text-sm font-semibold">支付网络</legend>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <button v-for="[id, method] in Object.entries(methods)" :key="id" type="button"
              :aria-pressed="selected === id" :disabled="method.available === false"
              class="flex items-center gap-3 rounded-xl border p-4 text-left disabled:opacity-40"
              :class="selected === id ? 'border-teal-500 bg-teal-50 dark:bg-teal-950/40' : 'border-gray-200 dark:border-dark-600'"
              @click="selected = id">
              <img v-if="networkIcons[id]" :src="networkIcons[id]" alt="" aria-hidden="true" width="32" height="32" class="h-8 w-8 shrink-0" />
              <svg v-else aria-hidden="true" viewBox="0 0 24 24" class="h-8 w-8 shrink-0 text-teal-600" fill="none" stroke="currentColor" stroke-width="1.5"><path d="m12 2 9 5v10l-9 5-9-5V7l9-5Zm0 0v20M3 7l9 5 9-5M3 17l9-5 9 5"/></svg>
              <span class="flex-1 font-medium">{{ method.display_name || id }}</span><span v-if="selected === id" class="text-teal-600" aria-hidden="true">✓</span>
            </button>
          </div>
          <p class="mt-3 text-xs leading-5 text-gray-500">转账网络必须与所选网络一致。请按收银台显示的准确金额转账，矿工费由付款钱包承担。</p>
        </fieldset>
      </div>
    </div>
    <div class="card space-y-3 p-6" aria-live="polite">
      <div v-if="loading" class="text-sm text-gray-500">正在获取实时汇率…</div>
      <p v-else-if="error" role="alert" class="text-sm text-amber-700 dark:text-amber-300">{{ error }} <button type="button" class="underline" @click="refreshQuote">重新获取</button></p>
      <template v-else-if="quote">
        <div class="flex justify-between text-sm"><span class="text-gray-500">支付金额</span><strong>{{ quote.amount }} USDT</strong></div>
        <div class="flex justify-between text-sm"><span class="text-gray-500">USDT / CNY 汇率</span><span>1 USDT = {{ quote.rate }} CNY</span></div>
        <div class="flex justify-between text-sm"><span class="text-gray-500">折算余额</span><span>{{ quote.base_credit }}</span></div>
        <div class="flex justify-between text-sm text-teal-600"><span>额外赠送 1%</span><strong data-testid="usdt-bonus">+{{ quote.bonus_credit }}</strong></div>
        <div class="flex justify-between border-t border-gray-100 pt-3 dark:border-dark-600"><span>合计到账余额</span><strong class="text-2xl text-teal-600" data-testid="usdt-total">{{ quote.total_credit }}</strong></div>
        <p class="text-xs leading-5 text-gray-400">汇率来源 {{ quote.source }} · {{ new Date(quote.quoted_at).toLocaleTimeString() }} 更新。订单创建后汇率与赠送额固定，报价有效期 5 分钟。</p>
      </template>
    </div>
    <button type="button" class="btn btn-primary w-full py-3.5" :disabled="busy || loading || !quote || expired"
      @click="submit">{{ busy ? '正在创建订单…' : expired ? '报价已过期，请刷新' : `确认充值 ${quote?.amount || amount || '0'} USDT` }}</button>
    <button v-if="expired && !busy" type="button" class="w-full text-sm text-teal-600" @click="refreshQuote">刷新汇率</button>
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { paymentAPI } from '@/api/payment'
import type { MethodLimit, UsdtRechargeQuote } from '@/types/payment'
import bnbChainIcon from '@/assets/icons/networks/bnb-chain.svg'
import ethereumIcon from '@/assets/icons/networks/ethereum.svg'
import tronIcon from '@/assets/icons/networks/tron.svg'
import polygonIcon from '@/assets/icons/networks/polygon.svg'
const networkIcons: Record<string, string> = {
  usdt_bep20: bnbChainIcon,
  usdt_erc20: ethereumIcon,
  usdt_trc20: tronIcon,
  usdt_polygon: polygonIcon,
}
const props = defineProps<{ methods: Record<string, MethodLimit>; busy: boolean }>()
const emit = defineEmits<{ submit: [amount: number, method: string, token: string] }>()
const amount = ref('10')
const selected = ref('')
const quote = ref<UsdtRechargeQuote | null>(null)
const loading = ref(false)
const error = ref('')
const now = ref(Date.now())
let generation = 0
let debounce: ReturnType<typeof setTimeout> | undefined
const timer = setInterval(() => { now.value = Date.now() }, 1000)
const expired = computed(() => !!quote.value && now.value >= Date.parse(quote.value.expires_at))
watch(() => props.methods, methods => {
  if (!methods[selected.value]) selected.value = Object.keys(methods).find(id => methods[id]?.available !== false) || ''
}, { immediate: true })
watch([amount, selected], () => {
  generation++
  quote.value = null
  error.value = ''
  clearTimeout(debounce)
  loading.value = true
  debounce = setTimeout(refreshQuote, 350)
}, { immediate: true })
async function refreshQuote() {
  const current = ++generation
  quote.value = null
  error.value = ''
  const value = Number(amount.value)
  if (!/^\d+(\.\d{1,2})?$/.test(amount.value) || !Number.isFinite(value) || value <= 0 || !selected.value) {
    loading.value = false
    error.value = '请输入大于 0 的 USDT 金额，最多两位小数，并选择支付网络。'
    return
  }
  loading.value = true
  try {
    const result = await paymentAPI.getUsdtQuote({ amount: value, payment_type: selected.value })
    if (current === generation) quote.value = result.data
  } catch (e: unknown) {
    if (current === generation) error.value = (e as { response?: { data?: { message?: string } } }).response?.data?.message || '暂时无法获取报价，请稍后重试。'
  } finally {
    if (current === generation) loading.value = false
  }
}
function submit() {
  if (!quote.value || expired.value || loading.value || props.busy) return
  emit('submit', Number(quote.value.amount), selected.value, quote.value.token)
}
onUnmounted(() => { generation++; clearTimeout(debounce); clearInterval(timer) })
</script>
