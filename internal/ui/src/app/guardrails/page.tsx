'use client'

import GuardrailsManager from '@/components/GuardrailsManager'

export default function GuardrailsPage() {
  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Guardrails</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            Manage workspace guardrails and reusable guardrail catalog entries.
          </p>
        </div>
      </div>
      <GuardrailsManager />
    </div>
  )
}
