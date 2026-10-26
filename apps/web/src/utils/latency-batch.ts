import type { NodeLatencyProbeResult } from '~/apis'

export function latencyRequestFailure(
  error: unknown,
): 'session' | 'target' | 'request' | 'service' | 'busy' | 'coreChanged' | 'expired' {
  const response = (error as { response?: { status?: number; errors?: { message?: string }[] } })?.response
  const message = response?.errors?.[0]?.message || (error instanceof Error ? error.message : '')
  if (response?.status === 401 || response?.status === 403 || /access denied/i.test(message)) return 'session'
  if (/latency_job_busy/.test(message)) return 'busy'
  if (/core_changed/.test(message)) return 'coreChanged'
  if (/latency_job_expired|test_job_timeout/.test(message)) return 'expired'
  if (/check_target_failed|resolve latency check target|tcp_check_url|parse config/i.test(message)) return 'target'
  if (/invalid id|at most 8|unsupported.*query|Missing node test result/i.test(message)) return 'request'
  return 'service'
}

// A sliding window avoids a slow node holding up the next seven results.
export async function testLatencyBatches(
  ids: string[],
  probe: (ids: string[]) => Promise<NodeLatencyProbeResult[]>,
  update: (results: NodeLatencyProbeResult[], completed: number, total: number) => void,
  stopped: () => boolean,
  wait: (ms: number) => Promise<void> = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
) {
  const unique = [...new Set(ids)]
  let completed = 0
  let next = 0
  let failure: unknown
  let workers = 8
  await Promise.all(
    Array.from({ length: Math.min(8, unique.length) }, async (_, worker) => {
      while (next < unique.length && !stopped() && !failure && worker < workers) {
        const id = unique[next++]
        for (let attempt = 0; attempt < 3 && !stopped() && !failure; attempt++) {
          try {
            const results = await probe([id])
            if (!results.some((result) => result.id === id)) throw new Error('Missing node test result')
            update(results, ++completed, unique.length)
            break
          } catch (error) {
            // Reduce new work under load. An unreachable node is a normal
            // result, while transport failures retain the unfinished queue.
            workers = 4
            if (latencyRequestFailure(error) !== 'service' || attempt === 2) {
              failure = error
              break
            }
            await wait(attempt === 0 ? 300 : 900)
          }
        }
      }
    }),
  )
  // Wait for all in-flight requests before enabling another test.
  if (failure) throw failure
}
