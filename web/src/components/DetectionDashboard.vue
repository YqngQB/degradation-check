<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import ServiceInfo from './ServiceInfo.vue'
import {
  clearResults,
  getServiceConfig,
  getAccounts,
  getResults,
  previewUrl,
  runTest,
  type Account,
  type Result,
  type TestType,
  type Variant
} from '../api'

const tests: { key: TestType; label: string }[] = [
  { key: 'candy', label: '糖果题' },
  { key: 'fingerprint', label: '指纹检测' },
  { key: 'pelican', label: '画鹈鹕' }
]
const efforts = ['low', 'medium', 'high', 'xhigh', 'max']

const PLATFORM_MODELS: Record<string, string[]> = {
  openai: ['gpt-6.1-sol', 'gpt-6-astra', 'gpt-6.1-luna', 'gpt-5.6-terra', 'gpt-5.6-sol', 'codex-auto-review'],
  anthropic: ['claude-opus-5-5', 'claude-sonnet-4-6', 'claude-haiku-4-5'],
  antigravity: ['claude-sonnet-4-6', 'claude-opus-5-5', 'gemini-2.5-pro'],
  gemini: ['gemini-2.5-pro', 'gemini-2.5-flash'],
  grok: ['grok-4.6', 'grok-4.5']
}

const ready = ref(false)
const loading = ref(false)
const submitting = ref(false)
const clearing = ref(false)
const error = ref('')
const showSettings = ref(false)
const previewId = ref<number | null>(null)

const serviceMode = ref<'direct' | 'sub2api'>('direct')
const instanceName = ref('降智检测')

const active = ref<TestType>('candy')
const accounts = ref<Account[]>([])
const results = ref<Result[]>([])
const running = ref<{ account_id: number; test_type: TestType }[]>([])
const variants = ref<Variant[]>([])
const syncedAt = ref('')

const platform = ref('')
const model = ref('gpt-6.1-sol')
const effort = ref('')
const runs = ref(1)
const variant = ref('original')

let timer: ReturnType<typeof setTimeout> | null = null

const platforms = computed(() => [...new Set(accounts.value.map((a) => a.platform).filter(Boolean))].sort())

const modelOptions = computed(() => {
  const configured = accounts.value.flatMap(a => a.models || [])
  if (configured.length) return [...new Set(configured)]
  if (platform.value) return PLATFORM_MODELS[platform.value] ?? ['gpt-6.1-sol']
  const all = new Set<string>()
  Object.values(PLATFORM_MODELS).forEach((list) => list.forEach((m) => all.add(m)))
  return [...all]
})

watch(platform, (p) => {
  const preferred = PLATFORM_MODELS[p]?.[0]
  if (preferred) model.value = preferred
})

const columns = computed(() =>
  active.value === 'pelican'
    ? 'minmax(0,1.3fr) 60px 190px 80px minmax(0,1.2fr) 110px 72px'
    : 'minmax(0,1.3fr) 60px 190px 80px minmax(0,1.3fr) 110px 72px'
)

const filtered = computed(() => (platform.value ? accounts.value.filter((a) => a.platform === platform.value) : accounts.value))

function platformMatches(p: string, m: string): boolean {
  // A relay may expose custom model aliases unrelated to provider prefixes.
  if (serviceMode.value === 'direct') return true
  const name = m.toLowerCase()
  switch (p.toLowerCase()) {
    case 'openai':
      return name.startsWith('gpt-') || name.startsWith('codex-')
    case 'anthropic':
      return name.startsWith('claude-')
    case 'antigravity':
      return name.startsWith('claude-') || name.startsWith('gemini-')
    case 'gemini':
      return name.startsWith('gemini-')
    case 'grok':
      return name.startsWith('grok-')
    default:
      return true
  }
}

const skippedByModel = computed(() => filtered.value.filter((a) => !platformMatches(a.platform, model.value)).length)

const runnable = computed(() => filtered.value.filter((a) => blockedReason(a) === ''))

function blockedReason(a: Account): string {
  if (a.status && a.status !== 'active') return `账号状态为 ${a.status}`
  if (a.schedulable === false) return '账号已被暂停调度'
  if (!platformMatches(a.platform, model.value)) return `模型 ${model.value} 不适用于 ${a.platform} 平台`
  return ''
}

function resultOf(id: number) {
  return results.value.find((r) => r.account_id === id && r.test_type === active.value)
}
function isRunning(id: number) {
  return running.value.some((r) => r.account_id === id && r.test_type === active.value)
}
function hasPreview(id: number) {
  const r = resultOf(id)
  return !!(r && r.payload && r.payload.html)
}
function previewSrc(id: number) {
  const r = resultOf(id)
  return previewUrl(id, r?.tested_at)
}
function attributionOf(id: number) {
  const r = resultOf(id)
  const a = r?.payload?.attribution
  return a && a.prediction ? a : null
}
function rateOf(id: number) {
  const r = resultOf(id)
  if (!r || !r.total_runs) return 0
  return Math.round((r.correct_runs / r.total_runs) * 100)
}

function statusLabel(s: string) {
  return (
    {
      correct: '答对',
      wrong: '答错',
      completed: '已生成',
      failed: '失败',
      skipped: '已跳过'
    } as Record<string, string>
  )[s] || s
}
function statusClass(s: string) {
  if (s === 'correct' || s === 'completed') return 'badge-success'
  if (s === 'wrong' || s === 'failed') return 'badge-error'
  return 'badge-secondary'
}
function platformClass(p: string) {
  switch (p) {
    case 'openai':
      return 'bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10 dark:text-emerald-400'
    case 'anthropic':
      return 'bg-amber-50 text-amber-600 dark:bg-amber-500/10 dark:text-amber-400'
    case 'antigravity':
      return 'bg-violet-50 text-violet-600 dark:bg-violet-500/10 dark:text-violet-400'
    default:
      return 'badge-secondary'
  }
}
function fmtNum(n: number) {
  return n ? Number(n).toLocaleString('en-US') : '0'
}
function fmtDur(ms: number) {
  if (!ms) return '—'
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}
function stamp() {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// ---------------- 数据加载 ----------------

async function reloadAll(withAccounts = false) {
  if (!ready.value) return
  loading.value = true
  try {
    if (withAccounts || accounts.value.length === 0) {
      const { accounts: list } = await getAccounts()
      accounts.value = list
    }
    const state = await getResults()
    results.value = state.results || []
    running.value = (state.running as any) || []
    variants.value = state.variants || []
    syncedAt.value = stamp()
    error.value = ''
  } catch (e: any) {
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
    schedulePoll()
  }
}

function schedulePoll() {
  if (timer) clearTimeout(timer)
  timer = null
  if (running.value.length > 0) {
    timer = setTimeout(() => reloadAll(false), 2500)
  }
}

async function initialize() {
  loading.value = true
  error.value = ''
  try {
    const config = await getServiceConfig()
    serviceMode.value = config.mode
    instanceName.value = config.instance
    ready.value = true
    await reloadAll(true)
  } catch (e: any) {
    ready.value = false
    error.value = e?.message || '加载失败，请检查服务端配置'
  } finally {
    loading.value = false
  }
}

async function runAll() {
  if (submitting.value) return
  submitting.value = true
  try {
    const targets = runnable.value
    const skipped = filtered.value.length - targets.length
    const res = await runTest({
      account_ids: targets.map((a) => a.id),
      test_type: active.value,
      model: model.value,
      effort: effort.value,
      variant: variant.value,
      runs: runs.value,
      accounts: targets.map((a) => ({ id: a.id, name: a.name, email: a.email, platform: a.platform, type: a.type }))
    })
    if (res.skipped > 0 && res.message) error.value = res.message
    else if (skipped > 0) error.value = `已跳过 ${skipped} 个不匹配或不活跃的账号`
    await reloadAll(false)
  } catch (e: any) {
    error.value = e?.message || '发起测试失败'
  } finally {
    submitting.value = false
  }
}

async function runOne(id: number) {
  const acc = accounts.value.find((a) => a.id === id)
  if (!acc) return
  try {
    await runTest({
      account_ids: [id],
      test_type: active.value,
      model: model.value,
      effort: effort.value,
      variant: variant.value,
      runs: runs.value,
      accounts: [{ id: acc.id, name: acc.name, email: acc.email, platform: acc.platform, type: acc.type }]
    })
    await reloadAll(false)
  } catch (e: any) {
    error.value = e?.message || '发起测试失败'
  }
}

async function clearAll() {
  if (!confirm('清空当前测试类型的全部记录？')) return
  clearing.value = true
  try {
    await clearResults(active.value)
    await reloadAll(false)
  } catch (e: any) {
    error.value = e?.message || '清空失败'
  } finally {
    clearing.value = false
  }
}

function openPreview(id: number) {
  previewId.value = id
}

onMounted(initialize)

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})
</script>

<template>
  <div class="min-h-screen bg-gray-50 dark:bg-dark-950">
    <!-- 顶部 -->
    <header class="sticky top-0 z-20 border-b border-gray-100 bg-white/90 px-6 py-3 backdrop-blur dark:border-dark-800 dark:bg-dark-900/90">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-3">
          <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-br from-primary-500 to-primary-600 text-sm font-bold text-white shadow-md shadow-primary-500/25">
            检
          </div>
          <div>
            <h1 class="text-base font-semibold leading-tight">{{ instanceName }}</h1>
            <p class="text-xs text-gray-500 dark:text-dark-400">
              糖果题 · 指纹归因 · 画鹈鹕 · 检测结果供人工复核
            </p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          <span v-if="ready" class="hidden text-xs text-gray-400 sm:inline dark:text-dark-500">
            {{ serviceMode === 'direct' ? '上游直连' : 'Sub2API 兼容模式' }}
          </span>
          <button class="btn btn-secondary" :disabled="loading" @click="reloadAll(true)">刷新</button>
          <button class="btn btn-secondary" @click="showSettings = true">连接说明</button>
        </div>
      </div>
    </header>

    <div v-if="!ready" class="mx-auto max-w-lg px-6 py-16" role="status">
      <h2 class="text-lg font-semibold">{{ loading ? '正在加载检测渠道…' : '无法加载检测服务' }}</h2>
      <p v-if="error" class="mt-3 text-sm text-red-600" role="alert">{{ error }}</p>
      <button class="btn btn-secondary mt-4" :disabled="loading" @click="initialize">重试</button>
    </div>

    <!-- 主内容 -->
    <main v-else class="mx-auto max-w-[1400px] space-y-4 px-4 py-5 sm:px-6">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="inline-flex gap-1 rounded-xl bg-gray-100 p-1 dark:bg-dark-800">
          <button
            v-for="t in tests"
            :key="t.key"
            class="rounded-lg px-4 py-1.5 text-sm font-medium transition-colors"
            :class="
              active === t.key
                ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
                : 'text-gray-500 hover:text-gray-900 dark:text-dark-400'
            "
            @click="active = t.key"
          >
            {{ t.label }}
          </button>
        </div>
        <div class="flex items-center gap-2">
          <span v-if="syncedAt" class="text-xs text-gray-400">更新于 {{ syncedAt }}</span>
          <button class="btn btn-secondary" :disabled="clearing" @click="clearAll">清空记录</button>
        </div>
      </div>

      <div v-if="error" role="alert" class="card border-l-4 border-l-red-500 px-5 py-3 text-sm text-red-600">{{ error }}</div>

      <div class="card p-4">
        <div class="flex flex-wrap items-end gap-3">
          <label class="w-36">
            <span class="mb-1 block text-xs font-medium text-gray-500">平台</span>
            <select v-model="platform" class="input">
              <option value="">全部平台</option>
              <option v-for="p in platforms" :key="p" :value="p">{{ p }}</option>
            </select>
          </label>

          <label class="w-56">
            <span class="mb-1 block text-xs font-medium text-gray-500">模型（可输入别名）</span>
            <input v-model="model" list="model-options" class="input" placeholder="输入上游支持的模型名" />
            <datalist id="model-options">
              <option v-for="m in modelOptions" :key="m" :value="m" />
            </datalist>
          </label>

          <label v-if="active !== 'fingerprint'" class="w-32">
            <span class="mb-1 block text-xs font-medium text-gray-500">推理强度</span>
            <select v-model="effort" class="input">
              <option value="">默认</option>
              <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
            </select>
          </label>

          <label v-if="active === 'candy'" class="w-28">
            <span class="mb-1 block text-xs font-medium text-gray-500">每账号次数</span>
            <input v-model.number="runs" type="number" min="1" max="10" class="input" />
          </label>

          <label v-if="active === 'pelican'" class="w-36">
            <span class="mb-1 block text-xs font-medium text-gray-500">版本</span>
            <select v-model="variant" class="input">
              <option v-for="v in variants" :key="v.id" :value="v.id">{{ v.label }}</option>
            </select>
          </label>

          <button
            class="btn btn-primary ml-auto"
            :disabled="submitting || runnable.length === 0"
            @click="runAll"
          >
            测试全部 ({{ runnable.length }})
          </button>
        </div>
        <p v-if="skippedByModel > 0" class="mt-3 text-xs text-amber-600">
          当前列表里有 {{ skippedByModel }} 个账号的平台与模型 <b>{{ model }}</b> 不匹配，批量测试会自动跳过。
        </p>
      </div>

      <p v-if="serviceMode === 'sub2api'" class="text-sm text-amber-700 dark:text-amber-400">
        兼容模式依赖 Sub2API 管理测试接口；官方 v0.2.18 的 Responses 路径会忽略检测题目。请使用上游直连模式，保留 Codex 的 Responses 配置。
      </p>
      <div class="card overflow-x-auto">
        <div class="grid gap-4 border-b border-gray-100 bg-gray-50/70 px-5 py-3 text-xs font-medium text-gray-500 dark:border-dark-700 dark:bg-dark-800/60" :style="{ gridTemplateColumns: columns }">
          <span>账号</span>
          <span>ID</span>
          <span>平台 / 类型</span>
          <span>状态</span>
          <span>最近一次</span>
          <span>{{ active === 'pelican' ? '画面' : active === 'fingerprint' ? '归因' : '正确率' }}</span>
          <span class="text-right">操作</span>
        </div>

        <div v-if="filtered.length === 0" class="px-5 py-12 text-center text-sm text-gray-400">没有符合条件的账号</div>

        <div
          v-for="acc in filtered"
          :key="acc.id"
          class="grid items-center gap-4 border-b border-gray-100 px-5 py-4 last:border-0 dark:border-dark-800"
          :style="{ gridTemplateColumns: columns }"
        >
          <div class="min-w-0">
            <div class="truncate text-sm font-medium">{{ acc.name || acc.email || '#' + acc.id }}</div>
            <div v-if="acc.email && acc.email !== acc.name" class="truncate text-xs text-gray-400">{{ acc.email }}</div>
          </div>

          <span class="font-mono text-xs text-gray-500">#{{ acc.id }}</span>

          <div class="flex flex-wrap gap-1">
            <span class="badge" :class="platformClass(acc.platform)">{{ acc.platform }}</span>
            <span class="badge badge-secondary">{{ acc.type }}</span>
            <span v-if="acc.protocol" class="badge badge-secondary">{{ acc.protocol === 'responses' ? 'Responses' : 'Chat Completions' }}</span>
            <span v-if="acc.plan_type" class="badge badge-secondary">{{ acc.plan_type }}</span>
          </div>

          <div class="text-xs">
            <span class="badge" :class="acc.status === 'active' ? 'badge-success' : 'badge-warning'">{{ acc.status }}</span>
          </div>

          <div class="min-w-0">
            <template v-if="isRunning(acc.id)">
              <span class="inline-flex items-center gap-1.5 text-sm text-primary-600">
                <span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
                测试中
              </span>
            </template>
            <template v-else-if="resultOf(acc.id)">
              <div class="flex flex-wrap items-center gap-2">
                <span class="badge" :class="statusClass(resultOf(acc.id)!.status)">{{ statusLabel(resultOf(acc.id)!.status) }}</span>
                <span class="truncate text-xs text-gray-500">{{ resultOf(acc.id)!.model }}</span>
              </div>
              <div v-if="resultOf(acc.id)!.error" class="mt-1 line-clamp-2 text-xs text-red-500" :title="resultOf(acc.id)!.error">
                {{ resultOf(acc.id)!.error }}
              </div>
              <div v-else class="mt-1 flex flex-wrap gap-x-2 text-xs text-gray-400">
                <span>{{ fmtDur(resultOf(acc.id)!.duration_ms) }}</span>
                <span v-if="resultOf(acc.id)!.input_tokens">↑{{ fmtNum(resultOf(acc.id)!.input_tokens) }}</span>
                <span v-if="resultOf(acc.id)!.output_tokens">↓{{ fmtNum(resultOf(acc.id)!.output_tokens) }}</span>
                <span v-if="resultOf(acc.id)!.reasoning_tokens">思考 {{ fmtNum(resultOf(acc.id)!.reasoning_tokens) }}</span>
              </div>
            </template>
            <span v-else class="text-xs text-gray-400">尚未测试</span>
            <details v-if="resultOf(acc.id)" class="mt-2 text-xs">
              <summary class="cursor-pointer text-gray-500 focus-visible:outline focus-visible:outline-2">查看回答与详情</summary>
              <pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-2 dark:bg-dark-900">{{ JSON.stringify(resultOf(acc.id)!.payload, null, 2) }}</pre>
            </details>
          </div>

          <div class="min-w-0 text-sm">
            <template v-if="active === 'pelican'">
              <button
                v-if="hasPreview(acc.id)"
                class="relative block h-[54px] w-[96px] overflow-hidden rounded-lg border border-gray-200 bg-slate-50 dark:border-dark-700"
                title="点击放大"
                @click="openPreview(acc.id)"
              >
                <iframe
                  :src="previewSrc(acc.id)"
                  sandbox="allow-scripts"
                  title="检测画面缩略图"
                  tabindex="-1"
                  class="pointer-events-none origin-top-left"
                  style="transform: scale(0.12); width: 800px; height: 450px"
                ></iframe>
              </button>
              <span v-else class="text-xs text-gray-400">—</span>
            </template>
            <template v-else-if="active === 'fingerprint'">
              <template v-if="attributionOf(acc.id)">
                <div class="truncate text-sm">{{ attributionOf(acc.id)!.prediction_name || '—' }}</div>
                <div class="text-xs text-gray-400">
                  {{ attributionOf(acc.id)!.probability != null ? (attributionOf(acc.id)!.probability * 100).toFixed(1) + '%' : '仅排名' }}
                </div>
              </template>
              <span v-else class="text-xs text-gray-400">—</span>
            </template>
            <template v-else>
              <template v-if="resultOf(acc.id) && resultOf(acc.id)!.total_runs > 0">
                <div class="font-medium">{{ rateOf(acc.id) }}%</div>
                <div class="text-xs text-gray-400">
                  {{ resultOf(acc.id)!.correct_runs }}/{{ resultOf(acc.id)!.total_runs }} 累计
                </div>
              </template>
              <span v-else class="text-xs text-gray-400">—</span>
            </template>
          </div>

          <div class="text-right">
            <button
              class="btn btn-primary px-3 py-1 text-xs"
              :disabled="isRunning(acc.id) || blockedReason(acc) !== ''"
              :title="blockedReason(acc)"
              @click="runOne(acc.id)"
            >
              {{ isRunning(acc.id) ? '测试中' : '测试' }}
            </button>
          </div>
        </div>
      </div>

      <p class="pb-6 text-center text-xs text-gray-400">
        结果保存在本服务本地 · 每个账号每类测试只留最新一条 · 正确率是历史累计
      </p>
    </main>

    <!-- 预览弹窗 -->
    <div v-if="previewId" class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-6" @click.self="previewId = null">
      <div class="flex max-h-full w-full max-w-5xl flex-col overflow-hidden rounded-2xl bg-white shadow-2xl dark:bg-dark-800">
        <div class="flex items-center justify-between border-b border-gray-100 px-5 py-3 dark:border-dark-700">
          <span class="text-sm font-medium">账号 #{{ previewId }} 的画面</span>
          <button class="btn btn-secondary px-3 py-1 text-xs" @click="previewId = null">关闭</button>
        </div>
        <iframe :src="previewSrc(previewId)" sandbox="allow-scripts" title="检测画面预览" class="h-[70vh] w-full"></iframe>
      </div>
    </div>

    <ServiceInfo v-if="showSettings" :mode="serviceMode" @close="showSettings = false" />
  </div>
</template>

