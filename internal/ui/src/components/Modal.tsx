import React from 'react'

interface ModalProps {
  title: string
  onClose: () => void
  children: React.ReactNode
}

export default function Modal({ title, onClose, children }: ModalProps) {
  return (
    <div
      style={{
        position: 'fixed', inset: 0, background: 'rgba(30,58,95,0.35)',
        display: 'flex', alignItems: 'flex-start', justifyContent: 'center',
        zIndex: 1000, padding: '2rem 1rem', overflowY: 'auto',
      }}
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div style={{
        background: '#ffffff', borderRadius: '10px', border: '1px solid #bfdbfe',
        boxShadow: '0 8px 32px rgba(37,99,235,0.15)', width: '100%', maxWidth: '600px',
        padding: '1.5rem',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.25rem' }}>
          <h2 style={{ fontSize: '1.1rem', fontWeight: 700, color: '#1e3a5f' }}>{title}</h2>
          <button
            onClick={onClose}
            style={{ background: 'none', border: 'none', cursor: 'pointer', fontSize: '1.2rem', color: '#94a3b8', lineHeight: 1 }}
            aria-label="Close"
          >×</button>
        </div>
        {children}
      </div>
    </div>
  )
}
