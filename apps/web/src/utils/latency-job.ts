import type { NodeLatencyProbeResult } from '~/apis'
import type { GQLClientInterface } from '~/contexts'

interface JobSnapshot {
  id: string
  phase: 'running' | 'stopping' | 'completed' | 'cancelled' | 'failed'
  code?: string
  cursor: number
  results: NodeLatencyProbeResult[]
}

// Router jobs survive short CGI responses. Only explicit user actions start a
// job; polling reads incremental results and never initiates network probes.
export async function testRouterLatencyJob(
  client: GQLClientInterface,
  ids: string[],
  update: (results: NodeLatencyProbeResult[]) => void,
  stopped: () => boolean,
  wait: (ms: number) => Promise<void> = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
) {
  const operation = Array.from(crypto.getRandomValues(new Uint8Array(16)), (v) => v.toString(16).padStart(2, '0')).join(
    '',
  )
  const request = async (query: string, variables: Record<string, unknown>) => {
    for (let attempt = 0; ; attempt++) {
      try {
        return await client.request<Record<string, string>>(query, variables)
      } catch (error) {
        const status = (error as { response?: { status?: number; errors?: unknown[] } })?.response
        if (attempt >= 2 || status?.errors?.length || status?.status === 401 || status?.status === 403) throw error
        await wait(attempt === 0 ? 250 : 750)
      }
    }
  }
  let id = ''
  let finished = false
  try {
    const start = await request(
      'mutation($ids:[ID!]!,$operation:String!){startLatencyTest(ids:$ids,operation:$operation)}',
      { ids, operation },
    )
    let snapshot = JSON.parse(start.startLatencyTest) as JobSnapshot
    id = snapshot.id
    if (!/^[a-f0-9]{32}$/.test(id)) throw new Error('Invalid test job response')
    let cancelSent = false
    for (;;) {
      if (snapshot.results.length) update(snapshot.results)
      if (snapshot.phase === 'completed' || snapshot.phase === 'cancelled') {
        finished = true
        return
      }
      if (snapshot.phase === 'failed') {
        finished = true
        throw new Error(snapshot.code || 'test_job_failed')
      }
      if (stopped() && !cancelSent) {
        await request('mutation($id:String!){stopLatencyTest(id:$id)}', { id })
        cancelSent = true
      }
      await wait(500)
      const data = await request('query($id:String!,$after:Int){latencyTest(id:$id,after:$after)}', {
        id,
        after: snapshot.cursor,
      })
      snapshot = JSON.parse(data.latencyTest) as JobSnapshot
    }
  } finally {
    if (id && !finished) {
      // A lost browser/session must not leave an unbounded diagnostic process.
      await request('mutation($id:String!){stopLatencyTest(id:$id)}', { id }).catch(() => {})
    }
  }
}
