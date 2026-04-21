import React from 'react'

interface CardProps {
  title?: string
  children: React.ReactNode
  style?: React.CSSProperties
}

export default function Card({ title, children, style }: CardProps) {
  return (
    <div style={{
      background: '#111d2e',
      border: '1px solid #1e3a5f',
      borderRadius: '6px',
      padding: '1rem',
      ...style,
    }}>
      {title && (
        <div style={{ fontSize: '0.8rem', fontWeight: 600, color: '#38bdf8', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.75rem' }}>
          {title}
        </div>
      )}
      {children}
    </div>
  )
}
