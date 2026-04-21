import React from 'react'

const chipStyle: React.CSSProperties = {
  background: '#1e3a5f',
  borderRadius: '4px',
  padding: '2px 6px',
  fontSize: '0.75rem',
  color: '#93c5fd',
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  border: '1px solid #1d4ed8',
}

const removeStyle: React.CSSProperties = {
  background: 'none',
  border: 'none',
  color: '#93c5fd',
  cursor: 'pointer',
  fontSize: '0.7rem',
  padding: '0',
  lineHeight: 1,
}

const selectStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 8px',
  border: '1px solid #1e3a5f',
  borderRadius: '6px',
  fontSize: '0.85rem',
  fontFamily: 'inherit',
  background: '#0f1d32',
  color: '#cbd5e1',
}

/**
 * BadgePicker — multi-select chip picker backed by a known options list.
 *
 * Selected values render as removable chips. The dropdown only shows
 * options not yet selected. When all options are selected the dropdown
 * is hidden. Falls back to a plain text field when no options are loaded
 * (e.g. store not configured or endpoint returned empty).
 */
export default function BadgePicker({
  options,
  selected,
  onChange,
  placeholder = 'Add…',
  freeText = false,
}: {
  options: string[]
  selected: string[]
  onChange: (v: string[]) => void
  placeholder?: string
  /** When true, show an extra free-text input alongside the picker. */
  freeText?: boolean
}) {
  const [text, setText] = React.useState('')
  const available = options.filter(o => !selected.includes(o))

  const remove = (v: string) => onChange(selected.filter(s => s !== v))
  const add = (v: string) => { if (v && !selected.includes(v)) onChange([...selected, v]) }

  const commitText = () => {
    const val = text.trim()
    if (val) { add(val); setText('') }
  }

  return (
    <div>
      {selected.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px', marginBottom: '6px' }}>
          {selected.map(v => (
            <span key={v} style={chipStyle}>
              {v}
              <button style={removeStyle} onClick={() => remove(v)} title={`Remove ${v}`}>✕</button>
            </span>
          ))}
        </div>
      )}
      {available.length > 0 && (
        <select
          value=""
          onChange={e => { if (e.target.value) add(e.target.value) }}
          style={selectStyle}
        >
          <option value="">{placeholder}</option>
          {available.map(o => <option key={o} value={o}>{o}</option>)}
        </select>
      )}
      {freeText && (
        <div style={{ display: 'flex', gap: '6px', marginTop: available.length > 0 ? '6px' : '0' }}>
          <input
            style={{ ...selectStyle, flex: 1 }}
            value={text}
            onChange={e => setText(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); commitText() } }}
            placeholder="Type and press Enter…"
          />
          <button
            onClick={commitText}
            style={{ padding: '6px 10px', border: '1px solid #1e3a5f', borderRadius: '6px', background: '#0f1d32', cursor: 'pointer', fontSize: '0.8rem', color: '#38bdf8' }}
          >
            Add
          </button>
        </div>
      )}
      {options.length === 0 && !freeText && (
        <p style={{ color: '#94a3b8', fontSize: '0.78rem', margin: '4px 0 0' }}>No options available (store not configured).</p>
      )}
    </div>
  )
}
