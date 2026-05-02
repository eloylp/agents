import React from 'react'

interface FullscreenModalProps {
  title: string
  onClose: () => void
  children: React.ReactNode
}

export default function FullscreenModal({ title, onClose, children }: FullscreenModalProps) {
  return (
    <div
      style={{
        position: 'fixed', inset: 0, background: 'var(--bg-modal-overlay)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1100, padding: '2vh 2vw',
      }}
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div style={{
        background: 'var(--bg-card)', borderRadius: '10px', border: '1px solid var(--border)',
        boxShadow: '0 8px 32px rgba(0,0,0,0.2)',
        width: '96vw', height: '96vh',
        display: 'flex', flexDirection: 'column',
      }}>
        <div style={{
          display: 'flex', justifyContent: 'space-between', alignItems: 'center',
          padding: '1rem 1.25rem', borderBottom: '1px solid var(--border)',
        }}>
          <h2 style={{ fontSize: '1.05rem', fontWeight: 700, color: 'var(--text-heading)' }}>{title}</h2>
          <button
            onClick={onClose}
            style={{ background: 'none', border: 'none', cursor: 'pointer', fontSize: '1.2rem', color: 'var(--text-faint)', lineHeight: 1 }}
            aria-label="Close"
          >x</button>
        </div>
        <div style={{ flex: 1, minHeight: 0, padding: '1rem 1.25rem', overflow: 'auto' }}>
          {children}
        </div>
      </div>
    </div>
  )
}
