'use client'
import { useState } from 'react'
import dynamic from 'next/dynamic'
import '@uiw/react-md-editor/markdown-editor.css'
import '@uiw/react-markdown-preview/markdown.css'
import { useTheme } from '@/lib/theme'
import FullscreenModal from './FullscreenModal'

const MDEditor = dynamic(() => import('@uiw/react-md-editor'), { ssr: false })

export default function MarkdownEditor({
  value, onChange, placeholder, minHeight = 200, expandable = true, expandTitle = 'Edit', readOnly = false,
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  minHeight?: number
  expandable?: boolean
  expandTitle?: string
  readOnly?: boolean
}) {
  const { theme } = useTheme()
  const [expanded, setExpanded] = useState(false)

  return (
    <div data-color-mode={theme} style={{ position: 'relative' }}>
      <MDEditor
        value={value}
        onChange={v => {
          if (!readOnly) onChange(v ?? '')
        }}
        preview="live"
        textareaProps={{ placeholder, readOnly }}
        height={minHeight}
        visibleDragbar
      />
      {expandable && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          aria-label="Expand editor"
          title="Expand editor"
          style={{
            position: 'absolute', top: 6, right: 8, zIndex: 2,
            background: 'var(--bg-input)', border: '1px solid var(--border)',
            color: 'var(--text)', padding: '2px 8px', borderRadius: '4px',
            cursor: 'pointer', fontSize: '0.75rem', lineHeight: 1.4,
          }}
        >⛶ Expand</button>
      )}
      {expanded && (
        <FullscreenModal title={expandTitle} onClose={() => setExpanded(false)}>
          <div data-color-mode={theme} style={{ height: '100%' }}>
            <MDEditor
              value={value}
              onChange={v => {
                if (!readOnly) onChange(v ?? '')
              }}
              preview="live"
              textareaProps={{ placeholder, readOnly }}
              height="100%"
              visibleDragbar={false}
            />
          </div>
        </FullscreenModal>
      )}
    </div>
  )
}
