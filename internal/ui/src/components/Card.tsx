import React from 'react'

interface CardProps {
  title?: string
  children: React.ReactNode
  style?: React.CSSProperties
}

export default function Card({ title, children, style }: CardProps) {
  return (
    <div style={{
      background: '#1e293b',
      border: '1px solid #334155',
      borderRadius: '8px',
      padding: '1rem',
      ...style,
    }}>
      {title && (
        <div style={{ fontSize: '0.8rem', fontWeight: 600, color: '#94a3b8', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.75rem' }}>
          {title}
        </div>
      )}
      {children}
    </div>
  )
}
