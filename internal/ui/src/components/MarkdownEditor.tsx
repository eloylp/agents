'use client'
import dynamic from 'next/dynamic'
import '@uiw/react-md-editor/markdown-editor.css'
import '@uiw/react-markdown-preview/markdown.css'
import { useTheme } from '@/lib/theme'

const MDEditor = dynamic(() => import('@uiw/react-md-editor'), { ssr: false })

export default function MarkdownEditor({
  value, onChange, placeholder, minHeight = 200,
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  minHeight?: number
}) {
  const { theme } = useTheme()

  return (
    <div data-color-mode={theme}>
      <MDEditor
        value={value}
        onChange={v => onChange(v ?? '')}
        preview="live"
        textareaProps={{ placeholder }}
        height={minHeight}
        visibleDragbar
      />
    </div>
  )
}
