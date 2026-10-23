/**
 * 与降智检测服务通信。服务端会把请求转发到 Sub2API，
 * 所以这里只需要带上 Sub2API 的地址和当前管理员令牌。
 */

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

export interface Credentials {
  base: string
  token: string
}

const TOKEN_KEY = 'dc.token'
const BASE_KEY = 'dc.base'

/** 从 URL（Sub2API 自定义菜单会带 token 和 src_host）或 sessionStorage 取凭据。 */
export function loadCredentials(): Credentials {
  const q = new URLSearchParams(location.search)
  const token = q.get('token') || sessionStorage.getItem(TOKEN_KEY) || ''
  const host = q.get('src_host') || q.get('host') || sessionStorage.getItem(BASE_KEY) || ''
  if (token) sessionStorage.setItem(TOKEN_KEY, token)
  if (host) sessionStorage.setItem(BASE_KEY, host)
  return { base: host, token }
}

export function saveCredentials(c: Credentials) {
  if (c.token) sessionStorage.setItem(TOKEN_KEY, c.token)
  if (c.base) sessionStorage.setItem(BASE_KEY, c.base)
}

let creds: Credentials = { base: '', token: '' }

export function setCredentials(c: Credentials) {
  creds = { base: c.base.replace(/\/+$/, ''), token: c.token }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      'X-Sub2API-Base': creds.base,
      'X-Sub2API-Token': creds.token,
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
    throw new Error(data?.error || `请求失败（HTTP ${res.status}）`)
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
