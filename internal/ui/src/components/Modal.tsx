import React from 'react'

interface ModalProps {
  title: React.ReactNode
  subtitle?: React.ReactNode
  onClose: () => void
  children: React.ReactNode
  maxWidth?: number | string
  maxHeight?: number | string
  zIndex?: number
  overlayBackground?: string
  align?: 'start' | 'center'
  showHeaderClose?: boolean
}

export default function Modal({
  title,
  subtitle,
  onClose,
  children,
  maxWidth = '600px',
  maxHeight,
  zIndex = 1200,
  overlayBackground = 'var(--bg-modal-overlay)',
  align = 'start',
  showHeaderClose = true,
}: ModalProps) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      style={{
        position: 'fixed', inset: 0, background: overlayBackground,
        display: 'flex', alignItems: align === 'center' ? 'center' : 'flex-start', justifyContent: 'center',
        zIndex, padding: align === 'center' ? '1rem' : '2rem 1rem', overflowY: 'auto',
      }}
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div style={{
        background: 'var(--bg-card)', borderRadius: '10px', border: '1px solid var(--border)',
        boxShadow: '0 8px 32px rgba(0,0,0,0.2)', width: '100%', maxWidth,
        maxHeight, overflow: maxHeight ? 'auto' : undefined, padding: '1.5rem',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.25rem' }}>
          <div>
            <h2 style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--text-heading)' }}>{title}</h2>
            {subtitle && <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem', marginTop: 4 }}>{subtitle}</div>}
          </div>
          {showHeaderClose && (
            <button
              onClick={onClose}
              style={{ background: 'none', border: 'none', cursor: 'pointer', fontSize: '1.2rem', color: 'var(--text-faint)', lineHeight: 1 }}
              aria-label="Close"
              title="Close"
            >×</button>
          )}
        </div>
        {children}
      </div>
    </div>
  )
}
