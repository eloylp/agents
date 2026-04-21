interface StatusBadgeProps {
  status: string
}

const colors: Record<string, { bg: string; color: string; border: string }> = {
  running: { bg: 'rgba(52,211,153,0.15)', color: '#34d399', border: '#065f46' },
  idle:    { bg: 'rgba(56,189,248,0.12)', color: '#38bdf8', border: '#0e7490' },
  error:   { bg: 'rgba(248,113,113,0.15)', color: '#f87171', border: '#7f1d1d' },
  success: { bg: 'rgba(52,211,153,0.15)', color: '#34d399', border: '#065f46' },
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
