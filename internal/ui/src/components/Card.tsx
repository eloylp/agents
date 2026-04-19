import React from 'react'

interface CardProps {
  title?: string
  children: React.ReactNode
  style?: React.CSSProperties
}

export default function Card({ title, children, style }: CardProps) {
  return (
    <div style={{
      background: '#ffffff',
      border: '1px solid #bfdbfe',
      borderRadius: '8px',
      padding: '1rem',
      boxShadow: '0 1px 2px rgba(37,99,235,0.06)',
      ...style,
    }}>
      {title && (
        <div style={{ fontSize: '0.8rem', fontWeight: 600, color: '#2563eb', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.75rem' }}>
          {title}
        </div>
      )}
      {children}
    </div>
  )
}
