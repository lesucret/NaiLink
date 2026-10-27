import { describe, expect, it } from 'vitest'
import { latencyRequestFailure, testLatencyBatches } from './latency-batch'

describe('bounded latency testing', () => {
  it('deduplicates IDs, uses concurrent single-node requests and reports each result', async () => {
    const batches: string[][] = []
    const progress: number[] = []
    await testLatencyBatches(
      [...Array.from({ length: 79 }, (_, i) => String(i)), '0'],
      async (ids) => {
        batches.push(ids)
        return ids.map((id) => ({ id, alive: true, latencyMs: 8, testedAt: '' }))
      },
      (_, count, total) => {
        expect(total).toBe(79)
        progress.push(count)
      },
      () => false,
    )
    expect(batches).toHaveLength(79)
    expect(batches.every((batch) => batch.length === 1)).toBe(true)
    expect(batches.flat()).toHaveLength(79)
    expect(progress).toEqual(Array.from({ length: 79 }, (_, i) => i + 1))
  })
  it('stops between batches and never submits an empty request', async () => {
    let calls = 0
    await testLatencyBatches(
      Array.from({ length: 100 }, (_, i) => String(i)),
      async ([id]) => {
        calls++
        return [{ id, alive: true, testedAt: '' }]
      },
      () => {},
      () => calls > 0,
    )
    expect(calls).toBe(1)
    await testLatencyBatches(
      [],
      async () => {
        calls++
        return []
      },
      () => {},
      () => false,
    )
    expect(calls).toBe(1)
  })
  it('retains completed batches and propagates transport failures without marking remaining nodes dead', async () => {
    const progress: number[] = []
    await expect(
      testLatencyBatches(
        Array.from({ length: 20 }, (_, i) => String(i)),
        async (ids) => {
          if (ids[0] === '8') throw new Error('HTTP 502')
          return ids.map((id) => ({ id, alive: false, testedAt: '' }))
        },
        (_, count) => progress.push(count),
        () => false,
        async () => {},
      ),
    ).rejects.toThrow('HTTP 502')
    expect(progress.length).toBeGreaterThan(0)
    expect(progress.length).toBeLessThan(20)
  })
  it('recovers transient failures across 132 nodes without repeating completed nodes', async () => {
    const attempts = new Map<string, number>()
    const completed: string[] = []
    await testLatencyBatches(
      Array.from({ length: 132 }, (_, i) => String(i)),
      async ([id]) => {
        const count = (attempts.get(id) || 0) + 1
        attempts.set(id, count)
        if (id === '65' && count < 3) throw new Error('HTTP 502')
        return [{ id, alive: Number(id) % 5 !== 0, testedAt: '' }]
      },
      (results) => completed.push(results[0].id),
      () => false,
      async () => {},
    )
    expect(completed).toHaveLength(132)
    expect(new Set(completed).size).toBe(132)
    expect(attempts.get('65')).toBe(3)
    expect(attempts.get('0')).toBe(1)
  })
  it('pauses persistent failures and can continue only unfinished IDs', async () => {
    const ids = Array.from({ length: 132 }, (_, i) => String(i))
    const completed = new Set<string>()
    let outage = true
    const probe = async ([id]: string[]) => {
      if (outage && Number(id) >= 65) throw new Error('HTTP 503')
      return [{ id, alive: true, testedAt: '' }]
    }
    const update = (results: { id: string }[]) => results.forEach((result) => completed.add(result.id))
    await expect(
      testLatencyBatches(
        ids,
        probe,
        update,
        () => false,
        async () => {},
      ),
    ).rejects.toThrow('503')
    expect(completed.size).toBeGreaterThan(0)
    expect(completed.size).toBeLessThan(132)
    const remaining = ids.filter((id) => !completed.has(id))
    outage = false
    await testLatencyBatches(remaining, probe, update, () => false)
    expect(completed.size).toBe(132)
  })
  it('does not retry expired sessions, invalid requests or DNS target failures', () => {
    expect(latencyRequestFailure({ response: { status: 401 } })).toBe('session')
    expect(latencyRequestFailure(new Error('cannot resolve latency check target'))).toBe('target')
    expect(latencyRequestFailure(new Error('invalid id'))).toBe('request')
    expect(latencyRequestFailure(new Error('HTTP 502'))).toBe('service')
    expect(latencyRequestFailure(new Error('core_ports_unavailable'))).toBe('ports')
    expect(latencyRequestFailure(new Error('core_ports_unavailable private://password'))).toBe('service')
  })
  it('stopping during retry backoff submits no extra probes', async () => {
    let stop = false
    let calls = 0
    await testLatencyBatches(
      ['1'],
      async () => {
        calls++
        throw new Error('network')
      },
      () => {},
      () => stop,
      async () => {
        stop = true
      },
    )
    expect(calls).toBe(1)
  })
  it('keeps eight workers moving while a slow node is pending, then drains in-flight work', async () => {
    let finish!: () => void
    const slow = new Promise<void>((resolve) => {
      finish = resolve
    })
    const completed: string[] = []
    let active = 0
    let peak = 0
    const task = testLatencyBatches(
      Array.from({ length: 12 }, (_, i) => String(i)),
      async ([id]) => {
        peak = Math.max(peak, ++active)
        if (id === '0') await slow
        await Promise.resolve()
        active--
        return [{ id, alive: true, latencyMs: 8, testedAt: '' }]
      },
      (results) => {
        completed.push(results[0].id)
      },
      () => false,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(peak).toBe(8)
    expect(completed).toHaveLength(11)
    expect(completed).not.toContain('0')
    finish()
    await task
    expect(completed.at(-1)).toBe('0')
    expect(active).toBe(0)
  })
})
