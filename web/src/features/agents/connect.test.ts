import { describe, expect, it } from 'vitest'
import { tunnelCommand } from '@/features/agents/commands'

describe('tunnelCommand', () => {
  it('fills in the server and the real token', () => {
    expect(tunnelCommand('https://l.example.com', 'lgr_abc', 'echo')).toBe('lagrange tunnel --server https://l.example.com --token lgr_abc --echo')
    expect(tunnelCommand('https://l.example.com', 'lgr_abc', 'to')).toContain('--token lgr_abc --to http://localhost:9001')
    expect(tunnelCommand('https://l.example.com', 'lgr_abc', 'acp')).toContain('--acp claude-agent-acp')
  })
})
