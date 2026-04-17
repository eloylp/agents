interface StatusBadgeProps {
  status: string
}

const colors: Record<string, { bg: string; color: string }> = {
  running: { bg: '#1d4ed8', color: '#bfdbfe' },
  idle:    { bg: '#374151', color: '#9ca3af' },
  error:   { bg: '#7f1d1d', color: '#fca5a5' },
  success: { bg: '#14532d', color: '#86efac' },
}

export default function StatusBadge({ status }: StatusBadgeProps) {
  const style = colors[status.toLowerCase()] ?? colors.idle
  return (
    <span style={{
      background: style.bg,
      color: style.color,
      padding: '2px 8px',
      borderRadius: '9999px',
      fontSize: '0.75rem',
      fontWeight: 600,
    }}>
      {status}
    </span>
  )
}
