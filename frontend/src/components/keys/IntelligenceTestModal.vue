<template>
  <BaseDialog :show="show" title="智商测试" width="wide" @close="$emit('close')">
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <span class="rounded-lg bg-emerald-50 px-3 py-1.5 text-sm text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300">{{ apiKey.group?.name }} · {{ apiKey.group?.rate_multiplier }}×</span>
        <span class="text-xs text-gray-500">{{ animation ? '生成动画，由你判断表现' : '两道题，看看模型这次的表现' }}</span>
      </div>
      <p v-if="loading" class="text-sm text-gray-500">正在读取分组模型…</p>
      <div v-else class="grid grid-cols-2 gap-3" aria-label="测试模型选择">
        <button v-for="m in options?.models || []" :key="m" type="button" :disabled="running || submitting" :aria-pressed="model === m" class="rounded-xl border px-4 py-3 text-sm transition-colors disabled:opacity-50" :class="model === m ? 'border-emerald-500 bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-200' : 'border-gray-200 text-gray-600 dark:border-dark-600 dark:text-dark-200'" @click="selectModel(m)">{{ intelligenceModelLabel(m) }}</button>
      </div>
      <p v-if="!loading && options && !options.models.length" class="text-sm text-amber-600">当前分组暂无支持的测试模型。</p>
      <div class="flex flex-wrap justify-between gap-2 text-xs text-gray-500">
        <span>{{ animation ? '一轮生成一个 HTML · 人工评阅' : '推理程度：轻度' }}</span>
        <span>独立上下文 · 不联网 · 不使用工具</span>
      </div>

      <div v-if="error" role="alert" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-200">{{ error }}</div>
      <div v-if="run?.result.error" role="alert" class="rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ run.result.error }}</div>
      <div class="flex justify-between gap-2 text-sm" aria-live="polite">
        <span>{{ running ? animation ? '正在生成 HTML…' : '正在检测…' : run?.status === 'completed' ? animation ? 'HTML 已生成，请自行观看判断' : '检测完成' : run?.status === 'failed' ? '本轮未完成' : '准备就绪' }}</span>
        <span class="tabular-nums text-gray-500">{{ elapsed }}<template v-if="!animation"> · {{ completedCount }} / 2 题</template></span>
      </div>
      <div v-if="!animation" class="h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" role="progressbar" aria-label="题目完成进度" :aria-valuenow="completedCount" :aria-valuemax="2" :aria-valuemin="0"><div class="h-full bg-emerald-500 transition-all" :style="{ width: `${completedCount * 50}%` }" /></div>

      <div v-for="q in questions" :key="q.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
        <div class="flex items-center justify-between gap-3"><span class="font-medium">{{ q.title }}</span><span v-if="!animation" class="text-sm" :class="q.status === 'passed' ? 'text-emerald-600 dark:text-emerald-300' : q.status === 'failed' ? 'text-red-600 dark:text-red-300' : 'text-gray-500'">{{ questionLabel(q.status) }}</span><span v-else class="text-xs text-gray-500">SVG · 2D 动画</span></div>
        <p v-if="!animation && q.answer && ['passed', 'failed'].includes(q.status)" class="mt-3 whitespace-pre-wrap break-words text-sm">{{ q.answer }}</p>
        <details class="mt-3 text-xs text-gray-500"><summary class="cursor-pointer">查看题目<template v-if="animation && q.answer">与原始回答</template></summary><p class="mt-2 whitespace-pre-wrap break-words leading-relaxed">{{ q.prompt }}</p><pre v-if="animation && q.answer" class="mt-3 max-h-72 overflow-auto whitespace-pre-wrap break-words">{{ q.answer }}</pre></details>
      </div>

      <template v-if="animation">
        <p v-if="scriptedAnimation" class="rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">这份答卷使用了脚本动画。安全预览未执行脚本，请下载 HTML 查看完整动画后再判断效果。</p>
        <iframe v-if="run?.result.html" :key="run.id" :srcdoc="preview" sandbox="" referrerpolicy="no-referrer" title="模型生成的 SVG 动画预览" class="h-80 w-full rounded-xl border border-gray-200 bg-white dark:border-dark-600" />
        <div v-else class="flex h-44 items-center justify-center rounded-xl border border-dashed border-gray-200 text-sm text-gray-500 dark:border-dark-600">{{ running ? '正在等待完整 HTML…' : '生成完成后，动画会在这里播放' }}</div>
        <div v-if="run?.result.html" class="space-y-3">
          <div class="flex flex-wrap gap-2"><button type="button" class="btn btn-secondary" @click="showCode = !showCode">{{ showCode ? '收起 HTML' : '查看 HTML' }}</button><button type="button" class="btn btn-secondary" @click="download">下载 HTML</button></div>
          <p class="break-all text-xs text-gray-500">{{ run.result.filename }} · 北京时间</p>
          <pre v-if="showCode" class="max-h-96 overflow-auto rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-800"><code>{{ run.result.html }}</code></pre>
          <p class="text-xs text-gray-500">预览支持 SVG / CSS 动画，禁用脚本与外部资源。请人工判断画面效果，生成成功不代表测试通过。</p>
        </div>
      </template>

      <div v-if="run" class="text-xs leading-relaxed text-gray-500">本轮输入 {{ run.result.input_tokens.toLocaleString() }} · 输出 {{ run.result.output_tokens.toLocaleString() }} token<span v-if="run.result.cache_read_tokens"> · 缓存读取 {{ run.result.cache_read_tokens.toLocaleString() }}</span><span v-if="run.result.cache_creation_tokens"> · 缓存写入 {{ run.result.cache_creation_tokens.toLocaleString() }}</span><br><span v-if="run.result.actual_cost != null">已入账费用 ${{ run.result.actual_cost.toFixed(6) }} · </span>费用按当前密钥的分组规则结算，以最终使用记录为准。</div>
      <button type="button" class="flex w-full items-center justify-center rounded-xl bg-emerald-600 px-4 py-3 font-medium text-white transition-colors hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-50" :disabled="!model || !options?.models.length || loading || running || submitting" @click="start">{{ submitting ? '正在创建任务…' : running ? '请等待本轮完成' : animation ? '生成 HTML' : '开始测试' }}</button>
      <p class="text-center text-xs leading-relaxed text-gray-500">按本轮实际总用量收费，以最终账单为准。单轮最长等待 5 分钟。<br>{{ animation ? '只生成 HTML，由你人工评阅。' : '结果仅反映本次题目表现；知识截止时间可能影响首相题。' }}</p>
      <details class="border-t border-gray-200 pt-4 dark:border-dark-600"><summary class="cursor-pointer text-sm">历史报告（最近 20 轮）</summary><p v-if="!history.length" class="mt-3 text-sm text-gray-500">暂无记录</p><div v-else class="mt-3 space-y-2"><button v-for="item in history" :key="item.id" type="button" :disabled="running || submitting" class="block w-full rounded-lg px-3 py-2 text-left text-sm hover:bg-gray-50 disabled:opacity-50 dark:hover:bg-dark-700" @click="loadHistory(item.id)">{{ localTime(item.created_at) }} · {{ intelligenceModelLabel(item.model) }} · {{ item.status === 'completed' ? item.kind === 'animation' ? '人工评阅' : '已完成' : item.status === 'running' ? '进行中' : '未完成' }}</button></div></details>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { intelligenceApi, intelligenceModelLabel, type IntelligenceOptions, type IntelligenceRun } from '@/api/intelligence'
import { buildIntelligencePreview, intelligenceDownload } from '@/utils/intelligencePreview'
import type { ApiKey } from '@/types'

const props = defineProps<{ show: boolean; apiKey: ApiKey }>()
defineEmits<{ (e: 'close'): void }>()
const options = ref<IntelligenceOptions | null>(null), run = ref<IntelligenceRun | null>(null), history = ref<IntelligenceRun[]>([])
const model = ref(''), loading = ref(false), submitting = ref(false), error = ref(''), showCode = ref(false), now = ref(Date.now())
let generation = 0, pollTimer: ReturnType<typeof setTimeout> | undefined
const clock = setInterval(() => { now.value = Date.now() }, 1000)
const animation = computed(() => props.apiKey.group?.platform === 'anthropic')
const running = computed(() => run.value?.status === 'running')
const questions = computed(() => run.value?.result.questions || options.value?.questions || [])
const completedCount = computed(() => questions.value.filter(q => ['passed', 'failed'].includes(q.status)).length)
const elapsed = computed(() => { if (!run.value) return '00:00'; const end = run.value.finished_at ? Date.parse(run.value.finished_at) : Math.min(now.value, Date.parse(run.value.deadline_at)); const seconds = Math.max(0, Math.floor((end - Date.parse(run.value.created_at)) / 1000)); return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}` })
const preview = computed(() => run.value?.result.html ? buildIntelligencePreview(run.value.result.html) : '')
const scriptedAnimation = computed(() => /<script\b|\son[a-z]+\s*=/i.test(run.value?.result.html || ''))
const localTime = (value: string) => new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })
const questionLabel = (value: string) => ({ passed: '通过', failed: '未通过', running: '检测中', pending: '待检测', error: '未完成' }[value] || '待检测')
const message = (err: unknown) => { const e = err as {message?: string; response?: {data?: {message?: string}}}; return e.response?.data?.message || e.message || '请求失败，请稍后再试' }
function selectModel(value: string) { if (running.value) return; model.value = value; run.value = null; showCode.value = false }
function schedulePoll(key: number, id: string, token: number) {
  clearTimeout(pollTimer)
  pollTimer = setTimeout(async () => {
    if (token !== generation || !props.show) return
    try {
      const result = await intelligenceApi.get(key, id)
      if (token !== generation) return
      run.value = result; error.value = ''
      if (result.status === 'running') schedulePoll(key, id, token)
      else {
        const rows = await intelligenceApi.history(key); if (token === generation) history.value = rows
        // Give the asynchronous billing ledger one final refresh after completion.
        pollTimer = setTimeout(async () => { try { const billed = await intelligenceApi.get(key, id); if (token === generation && run.value?.id === id) run.value = billed } catch { /* The report remains usable if billing is delayed. */ } }, 5000)
      }
    } catch (err) {
      if (token !== generation) return
      error.value = message(err)
      const status = (err as {response?: {status?: number}}).response?.status
      if (status && [401, 403, 404].includes(status)) return
      // Keep the job attached after a temporary network failure; never resubmit.
      schedulePoll(key, id, token)
    }
  }, 2500)
}
async function start() {
  if (!model.value || running.value || submitting.value) return
  const token = generation, key = props.apiKey.id
  submitting.value = true; error.value = ''; showCode.value = false
  try { const result = await intelligenceApi.start(key, model.value); if (token !== generation) return; run.value = result; schedulePoll(key, result.id, token) }
  catch (err) { if (token === generation) error.value = message(err) }
  finally { if (token === generation) submitting.value = false }
}
async function loadHistory(id: string) {
  const token = generation, key = props.apiKey.id
  error.value = ''; showCode.value = false
  try { const result = await intelligenceApi.get(key, id); if (token !== generation) return; run.value = result; model.value = result.model; if (result.status === 'running') schedulePoll(key, id, token) }
  catch (err) { if (token === generation) error.value = message(err) }
}
function download() { if (run.value?.result.html) intelligenceDownload(run.value.result.html, run.value.result.filename || 'pelican.html', run.value.created_at) }
watch(() => [props.show, props.apiKey.id] as const, async ([show, key]) => {
  const token = ++generation; clearTimeout(pollTimer)
  options.value = null; run.value = null; history.value = []; model.value = ''; error.value = ''; submitting.value = false; showCode.value = false
  if (!show) return
  loading.value = true
  const results = await Promise.allSettled([intelligenceApi.options(key), intelligenceApi.history(key)])
  if (token !== generation) return
  if (results[0].status === 'fulfilled') { options.value = results[0].value; model.value = results[0].value.models[0] || '' }
  else error.value = message(results[0].reason)
  if (results[1].status === 'fulfilled') { history.value = results[1].value; const active = history.value.find(item => item.status === 'running'); if (active) await loadHistory(active.id) }
  loading.value = false
}, { immediate: true })
onUnmounted(() => { generation++; clearTimeout(pollTimer); clearInterval(clock) })
</script>
