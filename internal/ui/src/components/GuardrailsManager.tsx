'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import FullscreenModal from '@/components/FullscreenModal'
import MarkdownEditor from '@/components/MarkdownEditor'

interface Guardrail {
  name: string
  description: string
  content: string
  default_content: string
  is_builtin: boolean
  enabled: boolean
  position: number
}

const emptyForm: Guardrail = {
  name: '',
  description: '',
  content: '',
  default_content: '',
  is_builtin: false,
  enabled: true,
  position: 100,
}

const inputStyle: React.CSSProperties = {
  width: '100%', padding: '6px 8px', border: '1px solid var(--border)', borderRadius: '6px',
  fontSize: '0.85rem', fontFamily: 'inherit', background: 'var(--bg-input)', color: 'var(--text)',
}
const labelStyle: React.CSSProperties = {
  fontSize: '0.8rem', color: 'var(--text-muted)', display: 'block', marginBottom: '3px',
}

function GuardrailForm({
  initial, isNew, onSave, onCancel, onReset, onDelete, saving, error,
}: {
  initial: Guardrail
  isNew: boolean
  onSave: (g: Guardrail) => void
  onCancel: () => void
  onReset?: () => void
  onDelete?: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<Guardrail>(initial)
  const set = <K extends keyof Guardrail>(k: K, v: Guardrail[K]) => setForm(f => ({ ...f, [k]: v }))

  const showReset = !isNew && form.is_builtin && !!onReset
  const canDelete = !isNew && !!onDelete

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      <div>
        <label style={labelStyle}>Name *</label>
        <input
          style={inputStyle}
          value={form.name}
          onChange={e => set('name', e.target.value)}
          placeholder="code-style"
          disabled={!isNew}
        />
        {!isNew && form.is_builtin && (
          <p style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '4px' }}>
            <strong>Built-in.</strong> Shipped with the daemon. Edits update the active content; the migration default is preserved and reachable via Reset.
          </p>
        )}
      </div>
      <div>
        <label style={labelStyle}>Description</label>
        <input
          style={inputStyle}
          value={form.description}
          onChange={e => set('description', e.target.value)}
          placeholder="Short label shown in the list"
        />
      </div>
      <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
        <label style={{ ...labelStyle, marginBottom: 0, display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.enabled} onChange={e => set('enabled', e.target.checked)} />
          Enabled (renderer prepends this guardrail)
        </label>
        <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: '6px' }}>
          <label style={{ ...labelStyle, marginBottom: 0 }}>Position</label>
          <input
            style={{ ...inputStyle, width: '80px' }}
            type="number"
            value={form.position}
            onChange={e => set('position', Number(e.target.value || 0))}
          />
        </div>
      </div>
      <div>
        <label style={labelStyle}>Content *</label>
        <MarkdownEditor
          value={form.content}
          onChange={v => set('content', v)}
          placeholder="The policy text prepended to every agent's composed prompt…"
          minHeight={260}
          expandTitle={isNew ? 'New guardrail' : `Edit ${form.name}`}
        />
      </div>
      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          {showReset && (
            <button
              onClick={onReset}
              style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
              title="Restore the migration's default content"
            >
              Reset to default
            </button>
          )}
          {canDelete && (
            <button
              onClick={onDelete}
              style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--text-danger)', background: 'transparent', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-danger)' }}
            >
              Delete
            </button>
          )}
        </div>
        <div style={{ display: 'flex', gap: '0.75rem' }}>
          <button
            onClick={onCancel}
            style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
          >
            Cancel
          </button>
          <button
            onClick={() => onSave(form)}
            disabled={saving || !form.name.trim() || !form.content.trim()}
            style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

export default function GuardrailsManager() {
  const [guardrails, setGuardrails] = useState<Guardrail[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')

  const [modal, setModal] = useState<'create' | 'edit' | 'delete-confirm' | 'disable-confirm' | null>(null)
  const [selected, setSelected] = useState<Guardrail>(emptyForm)
  const [confirmStep, setConfirmStep] = useState(0)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/guardrails')
      .then(r => r.json())
      .then((data: Guardrail[]) => {
        setGuardrails(data ?? [])
        setLoading(false)
      })
      .catch(e => { setLoadError(String(e)); setLoading(false) })
  }
  useEffect(load, [])

  const closeModal = () => {
    setModal(null)
    setSelected(emptyForm)
    setSaveError('')
    setConfirmStep(0)
  }

  const handleSave = async (g: Guardrail) => {
    setSaving(true)
    setSaveError('')
    try {
      const isNew = modal === 'create'
      const url = isNew ? '/guardrails' : `/guardrails/${encodeURIComponent(g.name)}`
      const method = isNew ? 'POST' : 'PATCH'
      const body = isNew
        ? { name: g.name, description: g.description, content: g.content, enabled: g.enabled, position: g.position }
        : { description: g.description, content: g.content, enabled: g.enabled, position: g.position }
      // Disabling a guardrail (especially a built-in) is sensitive — bounce
      // through a confirm modal before posting.
      if (!isNew && selected.enabled && !g.enabled) {
        setSelected(g)
        setSaving(false)
        setModal('disable-confirm')
        return
      }
      const res = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        setSaveError((await res.text()) || `${method} failed`)
        setSaving(false)
        return
      }
      load()
      closeModal()
    } catch (e) {
      setSaveError(String(e))
    } finally {
      setSaving(false)
    }
  }

  const confirmDisable = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch(`/guardrails/${encodeURIComponent(selected.name)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          description: selected.description, content: selected.content,
          enabled: false, position: selected.position,
        }),
      })
      if (!res.ok) {
        setSaveError((await res.text()) || 'PATCH failed')
        setSaving(false)
        return
      }
      load()
      closeModal()
    } catch (e) {
      setSaveError(String(e))
    } finally {
      setSaving(false)
    }
  }

  const handleReset = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch(`/guardrails/${encodeURIComponent(selected.name)}/reset`, { method: 'POST' })
      if (!res.ok) {
        setSaveError((await res.text()) || 'Reset failed')
        setSaving(false)
        return
      }
      load()
      closeModal()
    } catch (e) {
      setSaveError(String(e))
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch(`/guardrails/${encodeURIComponent(selected.name)}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        setSaveError((await res.text()) || 'Delete failed')
        setSaving(false)
        return
      }
      load()
      closeModal()
    } catch (e) {
      setSaveError(String(e))
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <p style={{ color: 'var(--text-muted)' }}>Loading guardrails…</p>
  if (loadError) return <p style={{ color: 'var(--text-danger)' }}>Error: {loadError}</p>

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <span style={{ color: 'var(--text-muted)', fontSize: '0.875rem' }}>
          {guardrails.length} guardrail{guardrails.length === 1 ? '' : 's'} — prepended to every agent's composed prompt in render order.
        </span>
        <button
          onClick={() => { setSelected(emptyForm); setModal('create') }}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          New guardrail
        </button>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
        {guardrails.map(g => (
          <div
            key={g.name}
            onClick={() => { setSelected(g); setModal('edit') }}
            style={{
              background: 'var(--bg-card)', border: '1px solid var(--border)',
              borderRadius: '8px', padding: '1rem',
              cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '0.75rem',
            }}
          >
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                <span style={{ fontWeight: 600, color: 'var(--text-heading)' }}>{g.name}</span>
                {g.is_builtin && (
                  <span style={{ fontSize: '0.7rem', padding: '2px 6px', borderRadius: '4px', background: 'var(--accent)', color: 'var(--bg-card)' }}>built-in</span>
                )}
                {!g.enabled && (
                  <span style={{ fontSize: '0.7rem', padding: '2px 6px', borderRadius: '4px', background: 'var(--bg-input)', color: 'var(--text-danger)', border: '1px solid var(--text-danger)' }}>disabled</span>
                )}
                <span style={{ fontSize: '0.7rem', color: 'var(--text-faint)' }}>position {g.position}</span>
              </div>
              {g.description && (
                <p style={{ fontSize: '0.8rem', color: 'var(--text-muted)', marginTop: '4px' }}>{g.description}</p>
              )}
            </div>
            <span style={{ color: 'var(--text-faint)', fontSize: '0.85rem' }}>edit →</span>
          </div>
        ))}
        {guardrails.length === 0 && (
          <p style={{ color: 'var(--text-muted)', fontStyle: 'italic' }}>No guardrails yet. The 'security' default should ship with the daemon — check that migrations applied.</p>
        )}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <FullscreenModal
          title={modal === 'create' ? 'New guardrail' : `Edit ${selected.name}`}
          onClose={closeModal}
        >
          <GuardrailForm
            initial={selected}
            isNew={modal === 'create'}
            onSave={handleSave}
            onCancel={closeModal}
            onReset={selected.is_builtin ? handleReset : undefined}
            onDelete={() => { setConfirmStep(0); setModal('delete-confirm') }}
            saving={saving}
            error={saveError}
          />
        </FullscreenModal>
      )}

      {modal === 'delete-confirm' && (
        <Modal title={`Delete ${selected.name}?`} onClose={closeModal}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            {selected.is_builtin ? (
              <>
                <p style={{ color: 'var(--text)', fontSize: '0.9rem' }}>
                  <strong>Warning:</strong> &lsquo;{selected.name}&rsquo; is a built-in guardrail shipped with the daemon. Deleting it removes the protection from every agent run until you re-create it. The migration cannot reseed it once deleted; you would need to copy the default text from <code>internal/store/migrations/010_guardrails.sql</code>.
                </p>
                <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
                  If you want to keep the row but skip rendering, use <strong>Disable</strong> from the edit panel instead.
                </p>
              </>
            ) : (
              <p style={{ color: 'var(--text)', fontSize: '0.9rem' }}>
                Delete &lsquo;{selected.name}&rsquo;? This removes the row from the database. Operator-added guardrails have no default to fall back to.
              </p>
            )}
            <label style={{ fontSize: '0.85rem', color: 'var(--text-muted)', display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
              <input type="checkbox" checked={confirmStep === 1} onChange={e => setConfirmStep(e.target.checked ? 1 : 0)} />
              Yes, I understand. Delete it.
            </label>
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button onClick={closeModal} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
                Cancel
              </button>
              <button
                onClick={handleDelete}
                disabled={confirmStep !== 1 || saving}
                style={{ padding: '6px 16px', borderRadius: '6px', border: 'none', background: confirmStep === 1 ? 'var(--text-danger)' : 'var(--bg-input)', color: '#fff', cursor: confirmStep === 1 && !saving ? 'pointer' : 'not-allowed', fontSize: '0.875rem', fontWeight: 600 }}
              >
                {saving ? 'Deleting…' : 'Delete'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {modal === 'disable-confirm' && (
        <Modal title={`Disable ${selected.name}?`} onClose={closeModal}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            {selected.is_builtin && selected.name === 'security' ? (
              <>
                <p style={{ color: 'var(--text)', fontSize: '0.9rem' }}>
                  <strong>Strong warning.</strong> Disabling the &lsquo;security&rsquo; guardrail removes the daemon's default defense against indirect prompt injection. Without it, a comment, issue body, or file content can instruct the agent to read auth files (e.g. <code>~/.claude.json</code>), exfiltrate secrets via comments or PRs, or contact attacker-controlled hosts. Only disable this if you have an alternate enforcement layer (sandbox, output filter, restrictive agent prompt) that closes the same gap.
                </p>
              </>
            ) : (
              <p style={{ color: 'var(--text)', fontSize: '0.9rem' }}>
                Disabling &lsquo;{selected.name}&rsquo; means the renderer skips it on every subsequent agent run. The row stays in the database; you can re-enable from this panel later.
              </p>
            )}
            <label style={{ fontSize: '0.85rem', color: 'var(--text-muted)', display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
              <input type="checkbox" checked={confirmStep === 1} onChange={e => setConfirmStep(e.target.checked ? 1 : 0)} />
              Yes, I understand the consequences. Disable it.
            </label>
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button onClick={closeModal} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
                Cancel
              </button>
              <button
                onClick={confirmDisable}
                disabled={confirmStep !== 1 || saving}
                style={{ padding: '6px 16px', borderRadius: '6px', border: 'none', background: confirmStep === 1 ? 'var(--text-danger)' : 'var(--bg-input)', color: '#fff', cursor: confirmStep === 1 && !saving ? 'pointer' : 'not-allowed', fontSize: '0.875rem', fontWeight: 600 }}
              >
                {saving ? 'Disabling…' : 'Disable'}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
