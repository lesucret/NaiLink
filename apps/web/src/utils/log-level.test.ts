import { describe, expect, it } from 'vitest'
import { splitLogLevels } from './log-level'

describe('log severity', () => {
  it('keeps text and multiline severity without interpreting message content', () => {
    const text =
      'time="Oct 25 13:00:00" level=warning msg="DNS failed"\ncontinuation\ntime="Oct 25 13:00:01" level=info msg="quoted level=error"\n'
    const entries = splitLogLevels(text)
    expect(entries.map((entry) => entry.level)).toEqual(['warning', 'warning', 'info'])
    expect(entries.map((entry) => entry.text).join('')).toBe(text)
  })
  it('supports structured JSON, unknown levels and markup as inert text', () => {
    const entries = splitLogLevels(
      '{"level":"error","msg":"<script>"}\n[WARN] retry\ntime="now" level=custom msg="x"\n',
    )
    expect(entries.map((entry) => entry.level)).toEqual(['error', 'warning', 'plain'])
    expect(splitLogLevels('')).toEqual([])
  })
  it('keeps error color across multiline JSON payloads', () => {
    const text = '[ERROR] failed\n{\n"detail": "failure"\n}\n{"payload":true}\n'
    const entries = splitLogLevels(text)
    expect(entries.every((entry) => entry.level === 'error')).toBe(true)
    expect(entries.map((entry) => entry.text).join('')).toBe(text)
  })
})
