interface StatusBadgeProps {
  status: string
}

const colors: Record<string, { bg: string; color: string; border: string }> = {
  running: { bg: '#f0fdf4', color: '#15803d', border: '#bbf7d0' },
  idle:    { bg: '#f0f9ff', color: '#0369a1', border: '#bae6fd' },
  error:   { bg: '#fef2f2', color: '#b91c1c', border: '#fecaca' },
  success: { bg: '#f0fdf4', color: '#15803d', border: '#bbf7d0' },
}

export default function StatusBadge({ status }: StatusBadgeProps) {
  const style = colors[status.toLowerCase()] ?? colors.idle
  return (
    <span style={{
      background: style.bg,
      color: style.color,
      border: `1px solid ${style.border}`,
      padding: '2px 8px',
      borderRadius: '9999px',
      fontSize: '0.75rem',
      fontWeight: 600,
    }}>
      {status}
    </span>
  )
}
