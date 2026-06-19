'use client'

interface PaginationControlsProps {
  total: number
  limit: number
  offset: number
  onLimitChange: (limit: number) => void
  onOffsetChange: (offset: number) => void
  pageSizes?: number[]
}

export default function PaginationControls({ total, limit, offset, onLimitChange, onOffsetChange, pageSizes = [25, 50, 100, 200] }: PaginationControlsProps) {
  const start = total === 0 ? 0 : offset + 1
  const end = Math.min(offset + limit, total)
  const prevDisabled = offset <= 0
  const nextDisabled = offset + limit >= total
  const buttonStyle = (disabled: boolean): React.CSSProperties => ({
    border: '1px solid var(--border)',
    borderRadius: '4px',
    background: disabled ? 'var(--bg-card)' : 'var(--bg)',
    color: disabled ? 'var(--text-muted)' : 'var(--text)',
    cursor: disabled ? 'not-allowed' : 'pointer',
    padding: '4px 8px',
    minWidth: '32px',
  })

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', color: 'var(--text-muted)', fontSize: '0.8rem' }}>
      <span>{start}-{end} of {total}</span>
      <select
        value={limit}
        onChange={e => onLimitChange(Number(e.target.value))}
        style={{ background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: '4px', color: 'var(--text)', padding: '4px 6px' }}
      >
        {pageSizes.map(size => <option key={size} value={size}>{size}</option>)}
      </select>
      <button disabled={prevDisabled} onClick={() => onOffsetChange(Math.max(0, offset - limit))} style={buttonStyle(prevDisabled)}>Prev</button>
      <button disabled={nextDisabled} onClick={() => onOffsetChange(offset + limit)} style={buttonStyle(nextDisabled)}>Next</button>
    </div>
  )
}
