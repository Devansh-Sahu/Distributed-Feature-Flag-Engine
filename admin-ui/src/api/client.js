// API client — all calls go through Vite's proxy to http://localhost:8080

const BASE = '/api/v1'

async function request(method, path, body) {
  const opts = {
    method,
    headers: { 'Content-Type': 'application/json' },
  }
  if (body !== undefined) opts.body = JSON.stringify(body)
  const res = await fetch(BASE + path, opts)
  const json = await res.json()
  if (!res.ok || !json.success) throw new Error(json.error || `HTTP ${res.status}`)
  return json.data
}

export const api = {
  // Flags
  listFlags:        ()            => request('GET',    '/flags'),
  createFlag:       (body)        => request('POST',   '/flags', body),
  updateFlagConfig: (key, env, b) => request('PATCH',  `/flags/${key}/config/${env}`, b),
  deleteFlag:       (key)         => request('DELETE', `/flags/${key}`),
  getAuditLog:      (key)         => request('GET',    `/flags/${key}/audit`),

  // Targeting rules
  createRule:  (key, body) => request('POST',   `/flags/${key}/rules`, body),
  deleteRule:  (key, ruleID) => request('DELETE', `/flags/${key}/rules/${ruleID}`),

  // State (bulk from Redis)
  getAllStates: (env) => request('GET', `/state/${env}`),

  // Environments
  listEnvironments: () => request('GET', '/environments'),

  // Health
  health: () => fetch('/health').then(r => r.json()),
}
