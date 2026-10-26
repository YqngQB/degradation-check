/** Credentials and upstream destinations are configured on the server only. */

export interface Account {
  id: number
  name: string
  email: string
  platform: string
  type: string
  status: string
  plan_type: string
  priority: number
  schedulable: boolean
  protocol?: 'responses' | 'chat_completions'
  models?: string[]
}

export type TestType = 'candy' | 'fingerprint' | 'pelican'

export interface Result {
  account_id: number
  test_type: TestType
  account_name: string
  account_email: string
  account_platform: string
  account_type: string
  model: string
  effort: string
  variant: string
  status: string
  error: string
  payload: Record<string, any>
  duration_ms: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  total_runs: number
  correct_runs: number
  tested_at: string
}

export interface Variant {
  id: string
  label: string
  format: string
}

export interface RunningTest {
  account_id: number
  test_type: TestType
  started_at: string
}

// Remove credentials left by older deployments. URL removal cannot erase
// an initial request already recorded by a reverse proxy; rotate exposed tokens.
for (const key of ['dc.token', 'dc.base']) sessionStorage.removeItem(key)
const url = new URL(location.href)
if (url.searchParams.has('token') || url.searchParams.has('src_host')) {
  url.searchParams.delete('token')
  url.searchParams.delete('src_host')
  history.replaceState(null, '', url.pathname + url.search + url.hash)
}

export interface ServiceConfig {
  mode: 'direct' | 'sub2api'
  instance: string
}

export function getServiceConfig(): Promise<ServiceConfig> {
  return request('/api/config')
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      'X-DC-Request': '1',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(init?.headers || {})
    }
  })
  const text = await res.text()
  let data: any = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) {
    throw new Error(res.status === 401 ? '登录已失效，请重新打开页面并登录检测服务' : data?.error || `请求失败（HTTP ${res.status}）`)
  }
  return data as T
}

export function getAccounts(): Promise<{ accounts: Account[] }> {
  return request('/api/accounts')
}

export function getResults(): Promise<{
  results: Result[]
  running: RunningTest[]
  variants: Variant[]
}> {
  return request('/api/results')
}

export function runTest(payload: {
  account_ids: number[]
  test_type: TestType
  model: string
  effort?: string
  variant?: string
  runs?: number
  accounts: Array<{ id: number; name: string; email: string; platform: string; type: string }>
}): Promise<{ started: number; skipped: number; message?: string }> {
  return request('/api/run', { method: 'POST', body: JSON.stringify(payload) })
}

export function clearResults(testType?: TestType): Promise<{ removed: number }> {
  const qs = testType ? `?test_type=${testType}` : ''
  return request(`/api/results${qs}`, { method: 'DELETE' })
}

export function previewUrl(accountId: number, stamp?: string): string {
  const v = stamp ? new Date(stamp).getTime() : Date.now()
  return `/api/preview/${accountId}?v=${v}`
}
