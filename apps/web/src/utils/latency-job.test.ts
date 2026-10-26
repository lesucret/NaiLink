import type { GQLClientInterface } from '~/contexts'
import { describe, expect, it } from 'vitest'
import { testRouterLatencyJob } from './latency-job'

const id = 'a'.repeat(32)
const result = (id: string) => ({ id, alive: true, latencyMs: 123, testedAt: '', source: 'manual' })
const status = (phase: string, cursor: number, ids: string[]) =>
  JSON.stringify({ id, phase, cursor, results: ids.map(result) })
describe('router background testing', () => {
  it('starts one idempotent job for 132 nodes and applies incremental results', async () => {
    const calls: string[] = []
    let operation = ''
    let starts = 0
    const client = {
      request: async (query: string, args: Record<string, unknown>) => {
        calls.push(query)
        if (query.includes('startLatencyTest')) {
          if (!starts++) {
            operation = String(args.operation)
            throw new Error('temporary connection failure')
          }
          expect(args.operation).toBe(operation)
          expect(args.ids).toHaveLength(132)
          return { startLatencyTest: status('running', 1, ['0']) }
        }
        expect(args.after).toBe(1)
        return {
          latencyTest: status(
            'completed',
            132,
            Array.from({ length: 131 }, (_, i) => String(i + 1)),
          ),
        }
      },
    } as unknown as GQLClientInterface
    const completed: string[] = []
    await testRouterLatencyJob(
      client,
      Array.from({ length: 132 }, (_, i) => String(i)),
      (r) => completed.push(...r.map((v) => v.id)),
      () => false,
      async () => {},
    )
    expect(completed).toHaveLength(132)
    expect(new Set(completed).size).toBe(132)
    expect(calls.filter((q) => q.includes('stopLatencyTest'))).toHaveLength(0)
    expect(calls).toHaveLength(3)
  })
  it('cancels in-flight work and retains only completed nodes for resume', async () => {
    const calls: string[] = []
    const client = {
      request: async (query: string) => {
        calls.push(query)
        if (query.includes('startLatencyTest')) return { startLatencyTest: status('running', 1, ['one']) }
        if (query.includes('stopLatencyTest')) return { stopLatencyTest: 1 }
        return { latencyTest: status('cancelled', 1, []) }
      },
    } as unknown as GQLClientInterface
    const completed: string[] = []
    await testRouterLatencyJob(
      client,
      ['one', 'two'],
      (r) => completed.push(...r.map((v) => v.id)),
      () => true,
      async () => {},
    )
    expect(completed).toEqual(['one'])
    expect(calls.filter((q) => q.includes('stopLatencyTest'))).toHaveLength(1)
  })
  it('keeps partial results when the core changes without marking pending nodes dead', async () => {
    const client = {
      request: async (query: string) =>
        query.includes('startLatencyTest')
          ? { startLatencyTest: status('running', 1, ['one']) }
          : { latencyTest: JSON.stringify({ ...JSON.parse(status('failed', 1, [])), code: 'core_changed' }) },
    } as unknown as GQLClientInterface
    const completed: string[] = []
    await expect(
      testRouterLatencyJob(
        client,
        ['one', 'two'],
        (r) => completed.push(...r.map((v) => v.id)),
        () => false,
        async () => {},
      ),
    ).rejects.toThrow('core_changed')
    expect(completed).toEqual(['one'])
  })
})
