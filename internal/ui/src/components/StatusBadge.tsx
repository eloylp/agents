interface StatusBadgeProps {
  status: string
}

const colors: Record<string, { bg: string; color: string; border: string }> = {
  running: { bg: 'var(--success-bg)', color: 'var(--success)', border: 'var(--success-border)' },
  idle:    { bg: 'var(--accent-bg)', color: 'var(--accent)', border: 'var(--btn-primary-border)' },
  error:   { bg: 'var(--error-bg)', color: 'var(--error)', border: 'var(--border-danger)' },
  success: { bg: 'var(--success-bg)', color: 'var(--success)', border: 'var(--success-border)' },
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
