import { useState, useEffect, useCallback, useRef } from 'react'
import { api } from './api/client.js'
import { useSSE } from './hooks/useSSE.js'

// ── Sub-components ────────────────────────────────────────────────

function Toggle({ enabled, onChange, loading }) {
  return (
    <div
      className="toggle"
      data-on={enabled ? 'true' : 'false'}
      onClick={(e) => { e.stopPropagation(); !loading && onChange(!enabled) }}
      style={{ cursor: loading ? 'wait' : 'pointer' }}
    >
      <div className="toggle-track" />
      <div className="toggle-thumb" />
    </div>
  )
}

function StatusPill({ enabled }) {
  return (
    <span className={`status-pill ${enabled ? 'enabled' : 'disabled'}`}>
      <span className="status-dot" />
      {enabled ? 'ENABLED' : 'DISABLED'}
    </span>
  )
}

function CreateFlagModal({ envs, onClose, onCreated }) {
  const [form, setForm] = useState({
    key: '', name: '', description: '', flag_type: 'boolean',
  })
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState(null)

  const set = (k, v) => setForm(f => ({ ...f, [k]: v }))

  const submit = async (e) => {
    e.preventDefault()
    if (!form.key || !form.name) return
    setLoading(true); setErr(null)
    try {
      const flag = await api.createFlag(form)
      onCreated(flag)
      onClose()
    } catch(e) { setErr(e.message) }
    finally { setLoading(false) }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={e => e.stopPropagation()}>
        <h2 className="modal-title">Create Feature Flag</h2>
        {err && <div className="error-banner">{err}</div>}
        <form onSubmit={submit}>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label">Flag Key *</label>
              <input type="text" value={form.key} placeholder="new-checkout-flow"
                onChange={e => set('key', e.target.value.toLowerCase().replace(/\s+/g,'-'))}
                style={{ fontFamily: 'var(--mono)' }} />
            </div>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label">Display Name *</label>
              <input type="text" value={form.name} placeholder="New Checkout Flow"
                onChange={e => set('name', e.target.value)} />
            </div>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label">Type</label>
              <select value={form.flag_type} onChange={e => set('flag_type', e.target.value)}>
                <option value="boolean">boolean</option>
                <option value="string">string</option>
                <option value="number">number</option>
                <option value="json">json</option>
              </select>
            </div>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label">Description</label>
              <input type="text" value={form.description} placeholder="Optional description"
                onChange={e => set('description', e.target.value)} />
            </div>
          </div>
          <div className="modal-actions">
            <button type="button" className="btn btn-ghost" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn btn-primary" disabled={loading || !form.key || !form.name}>
              {loading ? <span className="spinner" /> : '⚑'} Create Flag
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

function AddRuleForm({ flagKey, envId, onAdded, onCancel }) {
  const [form, setForm] = useState({
    priority: 0, attribute: '', operator: 'eq', value: '', serve_value: 'true',
  })
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState(null)
  const set = (k, v) => setForm(f => ({ ...f, [k]: v }))

  const submit = async (e) => {
    e.preventDefault()
    setLoading(true); setErr(null)
    try {
      let val, serve
      try { val = JSON.parse(form.value) } catch { val = form.value }
      try { serve = JSON.parse(form.serve_value) } catch { serve = form.serve_value }
      const rule = await api.createRule(flagKey, {
        environment_id: envId,
        priority: parseInt(form.priority) || 0,
        attribute: form.attribute,
        operator: form.operator,
        value: val,
        serve_value: serve,
      })
      onAdded(rule)
    } catch(e) { setErr(e.message) }
    finally { setLoading(false) }
  }

  return (
    <div className="add-rule-form">
      <div style={{ fontSize: '0.78rem', fontWeight: 600, color: 'var(--text-2)', marginBottom: 10 }}>
        Add Targeting Rule
      </div>
      {err && <div className="error-banner" style={{ marginBottom: 8 }}>{err}</div>}
      <form onSubmit={submit}>
        <div className="form-row">
          <div className="form-field" style={{ maxWidth: 70 }}>
            <label className="form-label">Priority</label>
            <input type="number" value={form.priority} min={0} max={100}
              onChange={e => set('priority', e.target.value)} />
          </div>
          <div className="form-field" style={{ flex: 2 }}>
            <label className="form-label">Attribute</label>
            <input type="text" value={form.attribute} placeholder="user.country"
              onChange={e => set('attribute', e.target.value)} required />
          </div>
        </div>
        <div className="form-row">
          <div className="form-field">
            <label className="form-label">Operator</label>
            <select value={form.operator} onChange={e => set('operator', e.target.value)}>
              {['eq','neq','in','not_in','gt','gte','lt','lte','contains','starts_with'].map(op =>
                <option key={op} value={op}>{op}</option>
              )}
            </select>
          </div>
          <div className="form-field" style={{ flex: 2 }}>
            <label className="form-label">Value (JSON or string)</label>
            <input type="text" value={form.value} placeholder={`"IN" or ["IN","US"]`}
              onChange={e => set('value', e.target.value)} required />
          </div>
        </div>
        <div className="form-row">
          <div className="form-field">
            <label className="form-label">Serve Value</label>
            <input type="text" value={form.serve_value} placeholder="true"
              onChange={e => set('serve_value', e.target.value)} required />
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 4 }}>
          <button type="button" className="btn btn-ghost btn-sm" onClick={onCancel}>Cancel</button>
          <button type="submit" className="btn btn-primary btn-sm" disabled={loading || !form.attribute}>
            {loading ? <span className="spinner" style={{width:12,height:12}} /> : '+'}
            {' '}Add Rule
          </button>
        </div>
      </form>
    </div>
  )
}

function FlagDetail({ flag, env, envId, onFlagUpdate }) {
  const [config, setConfig]         = useState(flag.configs?.[env] || {})
  const [rules, setRules]           = useState(flag.rules || [])
  const [audit, setAudit]           = useState([])
  const [loading, setLoading]       = useState({})
  const [rollout, setRollout]       = useState(config.rollout_percentage ?? 0)
  const [rolloutDirty, setRolloutDirty] = useState(false)
  const [showAddRule, setShowAddRule]   = useState(false)
  const [saving, setSaving]         = useState(false)

  const setLoad = (k, v) => setLoading(l => ({ ...l, [k]: v }))

  useEffect(() => {
    const c = flag.configs?.[env] || {}
    setConfig(c)
    setRules(flag.rules?.filter(r => r.environment_name === env || !r.environment_name) || [])
    setRollout(c.rollout_percentage ?? 0)
    setRolloutDirty(false)
    api.getAuditLog(flag.key).then(setAudit).catch(() => {})
  }, [flag.key, env])

  const toggleEnabled = async (val) => {
    setLoad('toggle', true)
    try {
      const updated = await api.updateFlagConfig(flag.key, env, { enabled: val })
      setConfig(c => ({ ...c, ...updated }))
      onFlagUpdate(flag.key, { ...config, ...updated })
    } catch(e) { console.error(e) }
    finally { setLoad('toggle', false) }
  }

  const saveRollout = async () => {
    setSaving(true)
    try {
      const updated = await api.updateFlagConfig(flag.key, env, { rollout_percentage: rollout })
      setConfig(c => ({ ...c, ...updated }))
      onFlagUpdate(flag.key, { ...config, ...updated })
      setRolloutDirty(false)
    } catch(e) { console.error(e) }
    finally { setSaving(false) }
  }

  const deleteRule = async (ruleId) => {
    setLoad(`rule_${ruleId}`, true)
    try {
      await api.deleteRule(flag.key, ruleId)
      setRules(r => r.filter(x => x.id !== ruleId))
    } catch(e) { console.error(e) }
    finally { setLoad(`rule_${ruleId}`, false) }
  }

  const actionColor = (a) =>
    a === 'created' ? 'created' : a === 'enabled' ? 'enabled' : a === 'disabled' ? 'disabled' : 'updated'

  const fmtTime = (ts) => {
    const d = new Date(ts)
    const now = new Date()
    const diff = (now - d) / 1000
    if (diff < 60) return `${Math.floor(diff)}s ago`
    if (diff < 3600) return `${Math.floor(diff/60)}m ago`
    if (diff < 86400) return `${Math.floor(diff/3600)}h ago`
    return d.toLocaleDateString()
  }

  const fmtVal = (v) => {
    try { return JSON.stringify(v) } catch { return String(v) }
  }

  return (
    <div>
      {/* Header */}
      <div className="detail-header">
        <div>
          <div className="detail-key">{flag.key}</div>
          <div className="detail-name">{flag.name}</div>
          {flag.description && <div className="detail-description">{flag.description}</div>}
        </div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <span className="flag-type-badge" style={{ fontSize: '0.72rem', padding: '3px 8px' }}>
            {flag.flag_type}
          </span>
          <StatusPill enabled={config.enabled} />
        </div>
      </div>

      {/* Kill Switch */}
      <div className={`killswitch-card ${config.enabled ? 'enabled' : 'disabled'}`}>
        <div className="killswitch-row">
          <div className="killswitch-label">
            <h3>Kill Switch</h3>
            <p>Instantly enables or disables this flag for all users in <strong style={{color:'var(--text-2)'}}>{env}</strong>. Propagates in &lt;2s via Kafka CDC → Redis pub/sub → SDK.</p>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            {loading.toggle && <span className="spinner" />}
            <Toggle enabled={!!config.enabled} onChange={toggleEnabled} loading={loading.toggle} />
          </div>
        </div>
      </div>

      {/* Rollout */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Rollout Percentage</span>
          {rolloutDirty && (
            <button
              className={`btn btn-primary btn-sm ${saving ? 'saving' : ''}`}
              onClick={saveRollout} disabled={saving}
            >
              {saving ? <span className="spinner" style={{width:12,height:12}} /> : '↑'}
              {' '}Save
            </button>
          )}
        </div>
        <div className="rollout-display">{rollout}%</div>
        <div className="rollout-sub">
          {rollout === 0 ? 'No users see this flag' :
           rollout === 100 ? 'All users see this flag' :
           `~${rollout}% of users see this flag (deterministic per user via FNV hash)`}
        </div>
        <input
          type="range" className="slider"
          min={0} max={100} value={rollout}
          onChange={e => { setRollout(Number(e.target.value)); setRolloutDirty(true) }}
        />
        <div className="rollout-labels">
          <span>0% — off</span>
          <span>50% — canary</span>
          <span>100% — full</span>
        </div>
      </div>

      {/* Targeting Rules */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Targeting Rules</span>
          <button className="btn btn-ghost btn-sm" onClick={() => setShowAddRule(r => !r)}>
            {showAddRule ? '✕ Cancel' : '+ Add Rule'}
          </button>
        </div>
        {rules.length === 0 && !showAddRule && (
          <p style={{ fontSize: '0.8rem', color: 'var(--text-3)', textAlign: 'center', padding: '12px 0' }}>
            No targeting rules. All users use rollout percentage.
          </p>
        )}
        {rules.map(rule => (
          <div className="rule-item" key={rule.id}>
            <div className="rule-priority">{rule.priority}</div>
            <div style={{ flex: 1, display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
              <span className="rule-attr">{rule.attribute}</span>
              <span className="rule-op">{rule.operator}</span>
              <span className="rule-val">{fmtVal(rule.value)}</span>
              <span className="rule-serve">→ serve</span>
              <span className="rule-serve-val">{fmtVal(rule.serve_value)}</span>
            </div>
            <button
              className="rule-del"
              onClick={() => deleteRule(rule.id)}
              disabled={loading[`rule_${rule.id}`]}
              title="Delete rule"
            >
              {loading[`rule_${rule.id}`] ? <span className="spinner" style={{width:12,height:12}} /> : '✕'}
            </button>
          </div>
        ))}
        {showAddRule && envId && (
          <AddRuleForm
            flagKey={flag.key}
            envId={envId}
            onAdded={(rule) => { setRules(r => [...r, rule]); setShowAddRule(false) }}
            onCancel={() => setShowAddRule(false)}
          />
        )}
      </div>

      {/* Audit Log */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Audit Log</span>
          <span className="count-badge">{audit.length}</span>
        </div>
        <div className="audit-timeline">
          {audit.slice(0, 20).map((entry, i) => (
            <div className="audit-item" key={entry.id}>
              <div className="audit-dot-col">
                <div className={`audit-dot ${actionColor(entry.action)}`} />
                {i < audit.length - 1 && <div className="audit-line" />}
              </div>
              <div style={{ flex: 1 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span className={`audit-action ${actionColor(entry.action)}`}>{entry.action}</span>
                  <span className="audit-time">{fmtTime(entry.created_at)}</span>
                </div>
                {entry.changed_by && (
                  <div style={{ fontSize: '0.72rem', color: 'var(--text-3)', marginTop: 2 }}>
                    by {entry.changed_by}
                  </div>
                )}
              </div>
            </div>
          ))}
          {audit.length === 0 && (
            <p style={{ fontSize: '0.8rem', color: 'var(--text-3)', textAlign: 'center', padding: '12px 0' }}>
              No audit events yet
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

// ── Main App ──────────────────────────────────────────────────────

export default function App() {
  const [env, setEnv]               = useState('production')
  const [envs, setEnvs]             = useState([])
  const [envMap, setEnvMap]         = useState({}) // name → {id}
  const [flags, setFlags]           = useState([])
  const [selectedKey, setSelectedKey] = useState(null)
  const [search, setSearch]         = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [loading, setLoading]       = useState(true)
  const [error, setError]           = useState(null)
  const [flashKeys, setFlashKeys]   = useState(new Set())

  const { lastUpdate, status: sseStatus } = useSSE(env)

  // Load flags and environments on mount and env change
  const loadData = useCallback(async () => {
    setLoading(true); setError(null)
    try {
      const [flagList, envList] = await Promise.all([
        api.listFlags(),
        api.listEnvironments(),
      ])
      setFlags(Array.isArray(flagList) ? flagList : (flagList?.flags || []))
      setEnvs(envList)
      const map = {}
      envList.forEach(e => { map[e.name] = e })
      setEnvMap(map)
    } catch(e) { setError(e.message) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { loadData() }, [loadData])

  // Handle live SSE flag updates
  useEffect(() => {
    if (!lastUpdate?.flag_key) return
    const key = lastUpdate.flag_key
    setFlags(prev => prev.map(f => {
      if (f.key !== key) return f
      // Merge the new config into the existing flag
      return {
        ...f,
        configs: {
          ...(f.configs || {}),
          [env]: {
            ...(f.configs?.[env] || {}),
            enabled: lastUpdate.enabled,
            rollout_percentage: lastUpdate.rollout_percentage,
          }
        }
      }
    }))
    // Flash animation
    setFlashKeys(s => new Set(s).add(key))
    setTimeout(() => setFlashKeys(s => {
      const ns = new Set(s); ns.delete(key); return ns
    }), 900)
  }, [lastUpdate, env])

  const selectedFlag = flags.find(f => f.key === selectedKey)

  const filteredFlags = flags.filter(f =>
    !search || f.key.toLowerCase().includes(search.toLowerCase()) ||
    f.name.toLowerCase().includes(search.toLowerCase())
  )

  const stats = {
    total:   flags.length,
    enabled: flags.filter(f => f.configs?.[env]?.enabled).length,
    disabled: flags.filter(f => !f.configs?.[env]?.enabled).length,
  }

  const onFlagCreated = (flag) => {
    setFlags(prev => [flag, ...prev])
    setSelectedKey(flag.key)
  }

  const onFlagUpdate = (key, config) => {
    setFlags(prev => prev.map(f => f.key !== key ? f : {
      ...f,
      configs: { ...(f.configs || {}), [env]: config }
    }))
  }

  return (
    <div className="app">
      {/* ── Header ── */}
      <header className="header">
        <div className="header-left">
          <div className="logo">
            <div className="logo-icon">⚑</div>
            <div>
              <div>FFEE</div>
              <div className="logo-sub">Feature Flag Engine</div>
            </div>
          </div>
        </div>
        <div className="header-right">
          <div className="live-indicator">
            <div className={`live-dot ${sseStatus}`} />
            {sseStatus === 'connected' ? 'Live' : sseStatus === 'connecting' ? 'Connecting…' : 'Reconnecting…'}
          </div>
          <div className="env-switcher">
            {['production','staging','development'].map(e => (
              <button
                key={e}
                className={`env-btn ${env === e ? `active ${e}` : ''}`}
                onClick={() => setEnv(e)}
              >
                {e}
              </button>
            ))}
          </div>
          <button className="btn btn-primary btn-sm" onClick={() => setShowCreate(true)}>
            + New Flag
          </button>
        </div>
      </header>

      {/* ── Main ── */}
      <main className="main">
        {/* ── Left: Flag list ── */}
        <div className="flags-panel">
          {/* Stats */}
          <div className="stats-bar">
            <div className="stat">
              <div className="stat-value">{stats.total}</div>
              <div className="stat-label">Total</div>
            </div>
            <div className="stat">
              <div className="stat-value green">{stats.enabled}</div>
              <div className="stat-label">Enabled</div>
            </div>
            <div className="stat">
              <div className="stat-value red">{stats.disabled}</div>
              <div className="stat-label">Disabled</div>
            </div>
          </div>

          {/* Panel header + search */}
          <div className="panel-header">
            <div className="panel-title">
              Feature Flags
              <span className="count-badge">{filteredFlags.length}</span>
            </div>
          </div>
          <div className="search-box">
            <input
              className="search-input"
              placeholder="Search flags…"
              value={search}
              onChange={e => setSearch(e.target.value)}
            />
          </div>

          {/* List */}
          <div className="flags-list">
            {loading && (
              <div style={{ display: 'flex', justifyContent: 'center', padding: 32 }}>
                <span className="spinner" style={{ width: 24, height: 24, borderWidth: 3 }} />
              </div>
            )}
            {error && <div className="error-banner" style={{ margin: 8 }}>{error}</div>}
            {!loading && filteredFlags.length === 0 && (
              <div style={{ textAlign: 'center', padding: 32, color: 'var(--text-3)', fontSize: '0.82rem' }}>
                {search ? 'No flags match your search' : 'No flags yet — create one!'}
              </div>
            )}
            {filteredFlags.map(flag => {
              const cfg = flag.configs?.[env] || {}
              const isActive = flag.key === selectedKey
              const isEnabled = cfg.enabled
              const isFlash = flashKeys.has(flag.key)
              return (
                <div
                  key={flag.key}
                  className={`flag-item ${isActive ? 'active' : ''} ${isEnabled ? 'enabled' : ''} ${isFlash ? 'updated' : ''}`}
                  onClick={() => setSelectedKey(flag.key)}
                >
                  <div className="flag-item-info">
                    <div className="flag-item-key">{flag.key}</div>
                    <div className="flag-item-meta">
                      <span className="flag-type-badge">{flag.flag_type || 'boolean'}</span>
                      {cfg.rollout_percentage != null && (
                        <span className="flag-rollout-mini">{cfg.rollout_percentage}%</span>
                      )}
                      {flag.rules?.length > 0 && (
                        <span className="flag-rollout-mini" style={{ color: 'var(--blue)' }}>
                          {flag.rules.length} rule{flag.rules.length !== 1 ? 's' : ''}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="flag-item-toggle" onClick={e => e.stopPropagation()}>
                    <Toggle
                      enabled={!!isEnabled}
                      onChange={async (val) => {
                        try {
                          await api.updateFlagConfig(flag.key, env, { enabled: val })
                          setFlags(prev => prev.map(f => f.key !== flag.key ? f : {
                            ...f,
                            configs: { ...(f.configs||{}), [env]: { ...cfg, enabled: val } }
                          }))
                        } catch(e) { console.error(e) }
                      }}
                    />
                  </div>
                </div>
              )
            })}
          </div>
        </div>

        {/* ── Right: Detail panel ── */}
        <div className="detail-panel">
          {selectedFlag ? (
            <FlagDetail
              key={`${selectedFlag.key}:${env}`}
              flag={selectedFlag}
              env={env}
              envId={envMap[env]?.id}
              onFlagUpdate={onFlagUpdate}
            />
          ) : (
            <div className="empty-state">
              <div className="empty-icon">⚑</div>
              <h3>Select a flag</h3>
              <p>Choose a feature flag from the list to manage its kill switch, rollout percentage, and targeting rules.</p>
            </div>
          )}
        </div>
      </main>

      {/* ── Modals ── */}
      {showCreate && (
        <CreateFlagModal
          envs={envs}
          onClose={() => setShowCreate(false)}
          onCreated={onFlagCreated}
        />
      )}
    </div>
  )
}
