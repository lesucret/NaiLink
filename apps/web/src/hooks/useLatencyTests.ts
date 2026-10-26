import type { NodeLatencyProbeResult } from '~/apis'
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useTestNodeLatenciesMutation } from '~/apis'
import { QUERY_KEY_NODE_LATENCY } from '~/constants'
import { useGQLQueryClient } from '~/contexts'
import { latencyRequestFailure, testLatencyBatches } from '~/utils/latency-batch'
import { testRouterLatencyJob } from '~/utils/latency-job'
import { routerMode } from '~/utils/router-mode'

interface TestRun {
  ids: string[]
  origin: string
  completed: Set<string>
  failed: Set<string>
}

export function useLatencyTests() {
  const { t } = useTranslation()
  const { mutateAsync } = useTestNodeLatenciesMutation()
  const client = useGQLQueryClient()
  const cache = useQueryClient()
  const busyRef = useRef(false)
  const stopped = useRef(false)
  const mounted = useRef(true)
  const run = useRef<TestRun | null>(null)
  const [pending, setPending] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [progress, setProgress] = useState({ completed: 0, total: 0 })
  const [error, setError] = useState('')
  const [scope, setScope] = useState('global')
  const [remaining, setRemaining] = useState(0)
  const [failed, setFailed] = useState(0)
  const [stopping, setStopping] = useState(false)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      stopped.current = true
    }
  }, [])
  const execute = useCallback(
    async (current: TestRun) => {
      const ids = current.ids.filter((id) => !current.completed.has(id))
      if (busyRef.current || !ids.length) return
      run.current = current
      busyRef.current = true
      stopped.current = false
      setError('')
      setScope(current.origin)
      setStopping(false)
      setBusy(true)
      setProgress({ completed: current.completed.size, total: current.ids.length })
      setRemaining(ids.length)
      setFailed(current.failed.size)
      const update = (results: NodeLatencyProbeResult[]) => {
        for (const result of results) {
          current.completed.add(result.id)
          if (result.alive) current.failed.delete(result.id)
          else current.failed.add(result.id)
        }
        if (mounted.current) {
          setProgress({ completed: current.completed.size, total: current.ids.length })
          setRemaining(current.ids.length - current.completed.size)
          setFailed(current.failed.size)
          cache.setQueryData<NodeLatencyProbeResult[]>(QUERY_KEY_NODE_LATENCY, (previous = []) => {
            const merged = new Map(previous.map((r) => [r.id, r]))
            for (const r of results) merged.set(r.id, r)
            return [...merged.values()]
          })
        }
      }
      try {
        if (routerMode) {
          await testRouterLatencyJob(client, ids, update, () => stopped.current)
        } else
          await testLatencyBatches(
            ids,
            async (batch) => {
              if (mounted.current) setPending((previous) => new Set([...previous, ...batch]))
              try {
                return await mutateAsync(batch)
              } finally {
                if (mounted.current)
                  setPending((previous) => new Set([...previous].filter((id) => !batch.includes(id))))
              }
            },
            update,
            () => stopped.current,
          )
      } catch (error) {
        if (mounted.current) setError(t(`latency.failure.${latencyRequestFailure(error)}`))
      } finally {
        busyRef.current = false
        if (mounted.current) {
          setPending(new Set())
          setBusy(false)
          setStopping(false)
        }
      }
    },
    [mutateAsync, t, client, cache],
  )
  const test = useCallback(
    (ids: string[], origin = 'global') =>
      execute({ ids: [...new Set(ids)], origin, completed: new Set(), failed: new Set() }),
    [execute],
  )
  const resume = useCallback(() => {
    if (run.current) return execute(run.current)
  }, [execute])
  const retryFailed = useCallback(() => {
    if (run.current) return test([...run.current.failed], run.current.origin)
  }, [test])
  const stop = useCallback(() => {
    stopped.current = true
    if (mounted.current && busyRef.current) setStopping(true)
  }, [])
  return {
    test,
    pending,
    busy,
    progress,
    scope,
    error,
    stop,
    remaining,
    failed,
    stopping,
    resume,
    retryFailed,
  }
}
