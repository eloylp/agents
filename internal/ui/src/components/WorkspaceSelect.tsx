'use client'

import { useSelectedWorkspace } from '@/lib/workspace'

interface Props {
  label?: string
  compact?: boolean
}

export default function WorkspaceSelect({ label = 'Workspace', compact = false }: Props) {
  const { workspace, workspaces, workspaceNotice, setWorkspace } = useSelectedWorkspace()

  return (
    <div style={{ display: 'flex', flexDirection: compact ? 'row' : 'column', gap: compact ? '0.4rem' : '0.25rem', alignItems: compact ? 'center' : 'stretch' }}>
      <label style={{ fontSize: '0.72rem', color: 'var(--text-muted)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
        {label}
      </label>
      <select
        value={workspace}
        onChange={e => setWorkspace(e.target.value)}
        title={label}
        style={{
          minWidth: '150px',
          background: 'var(--bg-card)',
          border: '1px solid var(--border)',
          borderRadius: 4,
          padding: '5px 8px',
          fontSize: '0.8rem',
          color: 'var(--text-muted)',
        }}
      >
        {workspaces.length === 0 && <option value={workspace}>{workspace}</option>}
        {workspaces.map(w => <option key={w.id} value={w.id}>{w.name || w.id}</option>)}
      </select>
      {workspaceNotice && (
        <span style={{ color: 'var(--text-muted)', fontSize: '0.72rem', maxWidth: '18rem' }}>
          {workspaceNotice}
        </span>
      )}
    </div>
  )
}
