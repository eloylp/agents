import { describe, expect, it } from 'vitest'
import { buildSetupChecks, githubMCPConnected, inferredBackends, selectedBackends, setupComplete, type BackendsDiagnostics } from './tooling-setup'

describe('selectedBackends', () => {
  it('defaults to both supported first-run backends', () => {
    expect(selectedBackends(null)).toEqual(['claude', 'codex'])
    expect(selectedBackends('both')).toEqual(['claude', 'codex'])
  })

  it('supports single-backend setup choices', () => {
    expect(selectedBackends('claude')).toEqual(['claude'])
    expect(selectedBackends('codex')).toEqual(['codex'])
  })
})

describe('inferredBackends', () => {
  it('uses explicit stored choices when present', () => {
    expect(inferredBackends({ backends: [{ name: 'claude', detected: true }] }, 'codex')).toEqual(['codex'])
  })

  it('infers an established single-backend setup from diagnostics', () => {
    expect(inferredBackends({ backends: [{ name: 'claude', detected: true }] }, null)).toEqual(['claude'])
  })

  it('falls back to selected backends when no stored choice or configured backend exists', () => {
    expect(inferredBackends({ backends: [{ name: 'claude', detected: false, models: [] }] }, null)).toEqual(['claude', 'codex'])
    expect(inferredBackends(undefined, undefined)).toEqual(['claude', 'codex'])
  })
})

describe('githubMCPConnected', () => {
  it('detects connected GitHub MCP detail case-insensitively', () => {
    expect(githubMCPConnected({ name: 'claude', health_detail: 'version: ok | GitHub MCP: Connected' })).toBe(true)
  })

  it('rejects disconnected or missing MCP details', () => {
    expect(githubMCPConnected({ name: 'claude', health_detail: 'github MCP: found but disconnected' })).toBe(false)
    expect(githubMCPConnected(undefined)).toBe(false)
  })
})

describe('setupComplete', () => {
  const healthyDiag: BackendsDiagnostics = {
    tools: [{ name: 'github_cli', detected: true, authenticated: true, healthy: true, detail: 'authenticated' }],
    backends: [
      { name: 'claude', detected: true, healthy: true, models: ['claude-sonnet'], health_detail: 'version: ok | github MCP: connected' },
      { name: 'codex', detected: true, healthy: true, models: ['gpt-5.4'], health_detail: 'version: ok | github MCP: connected' },
    ],
  }

  it('passes when selected tools are installed, authenticated, connected, and discovered', () => {
    expect(setupComplete(healthyDiag, ['claude'])).toBe(true)
    expect(setupComplete(healthyDiag, ['claude', 'codex'])).toBe(true)
  })

  it('fails only when an unhealthy backend is selected', () => {
    const diag: BackendsDiagnostics = {
      ...healthyDiag,
      backends: [
        { name: 'claude', detected: true, healthy: true, models: ['claude-sonnet'], health_detail: 'version: ok | github MCP: connected' },
        { name: 'codex', detected: true, healthy: false, models: ['gpt-5.4'], health_detail: 'codex auth missing' },
      ],
    }

    expect(setupComplete(diag, ['claude'])).toBe(true)
    expect(setupComplete(diag, ['claude', 'codex'])).toBe(false)
  })

  it('fails when a selected backend has no discovered models', () => {
    const diag: BackendsDiagnostics = {
      ...healthyDiag,
      backends: [{ name: 'claude', detected: true, healthy: true, models: [], health_detail: 'version: ok | github MCP: connected' }],
    }
    const checks = buildSetupChecks(diag, ['claude'])
    expect(checks.find(check => check.key === 'claude_models')?.ok).toBe(false)
    expect(setupComplete(diag, ['claude'])).toBe(false)
  })
})
