import React from 'react'

interface CardProps {
  title?: string
  children: React.ReactNode
  style?: React.CSSProperties
}

export default function Card({ title, children, style }: CardProps) {
  return (
    <div style={{
      background: 'var(--bg-card)',
      border: '1px solid var(--border)',
      borderRadius: '8px',
      padding: '1rem',
      ...style,
    }}>
      {title && (
        <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.75rem' }}>
          {title}
        </div>
      )}
      {children}
    </div>
  )
}
