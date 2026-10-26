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

// Accept the original menu link once; keep only an opaque detector session.
function stored(key: string): string { try { return sessionStorage.getItem(key) || '' } catch { return '' } }
function save(key: string, value: string) { try { if (value) sessionStorage.setItem(key, value); else sessionStorage.removeItem(key) } catch {} }
const entry = new URL(location.href)
let entryToken = entry.searchParams.get('token') || stored('dc.token')
let entrySource = entry.searchParams.get('src_host') || entry.searchParams.get('host') || stored('dc.base')
for (const key of ['token', 'src_host', 'host']) entry.searchParams.delete(key)
history.replaceState(null, '', entry.pathname + entry.search + entry.hash)
save('dc.token', ''); save('dc.base', '')
let session = stored('dc.session')

export interface ServiceConfig {
  mode: 'auto' | 'direct' | 'sub2api'
  instance: string
  sub2api_url?: string
}
export class APIError extends Error {
  constructor(message: string, public status: number, public code = '') { super(message) }
}
export function hasSession() { return !!session }
export function clearSession() { session = ''; save('dc.session', '') }
export async function connect(token: string, source = '') {
  const data = await request<{session: string}>('/api/session', { method: 'POST', body: JSON.stringify({token, src_host: source}) })
  session = data.session; save('dc.session', session)
}
export async function connectFromMenu() {
  const token = entryToken, source = entrySource
  entryToken = ''; entrySource = ''
  if (token) await connect(token, source)
}
export function getServiceConfig(): Promise<ServiceConfig> { return request('/api/config') }
export function verifyStepUp(code: string): Promise<{verified: boolean}> {
  return request('/api/step-up', {method: 'POST', body: JSON.stringify({code})})
}
async function response(path: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      'X-DC-Request': '1',
      ...(session ? {'X-DC-Session': session} : {}),
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(init?.headers || {})
    }
  })
  if (!res.ok) {
    const data = await res.json().catch(() => null)
    if (res.status === 401) clearSession()
    throw new APIError(data?.error || `请求失败（HTTP ${res.status}）`, res.status, data?.code)
  }
  return res
}
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  return (await response(path, init)).json()
}
export async function getPreview(accountId: number): Promise<string> {
  const html = await (await response(`/api/preview/${accountId}`)).text()
  // srcdoc does not inherit the fetched response's CSP header. Reapply it before
  // any generated content, and also sandbox the frame without allow-same-origin.
  return `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; base-uri 'none'; form-action 'none'">` + html
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
