'use client'

import type { ReactNode } from 'react'
import PaginationControls from '@/components/PaginationControls'

interface PaginatedDataSectionProps {
  total: number
  limit: number
  offset: number
  onLimitChange: (limit: number) => void
  onOffsetChange: (offset: number) => void
  children: ReactNode
  pageSizes?: number[]
  topLeft?: ReactNode
  style?: React.CSSProperties
  contentStyle?: React.CSSProperties
}

export default function PaginatedDataSection({
  total,
  limit,
  offset,
  onLimitChange,
  onOffsetChange,
  children,
  pageSizes,
  topLeft,
  style,
  contentStyle,
}: PaginatedDataSectionProps) {
  const controls = (
    <PaginationControls
      total={total}
      limit={limit}
      offset={offset}
      onLimitChange={onLimitChange}
      onOffsetChange={onOffsetChange}
      pageSizes={pageSizes}
    />
  )

  return (
    <section style={{ display: 'grid', gap: '0.85rem', ...style }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '1rem', flexWrap: 'wrap' }}>
        <div>{topLeft}</div>
        <div style={{ marginLeft: 'auto' }}>{controls}</div>
      </div>
      <div style={contentStyle}>{children}</div>
      <div style={{ display: 'flex', justifyContent: 'center' }}>
        {controls}
      </div>
    </section>
  )
}
