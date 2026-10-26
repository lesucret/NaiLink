import type { useLatencyTests } from '~/hooks/useLatencyTests'
import { CircleHelp } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from './ui/button'
import { Popover, PopoverContent, PopoverTrigger } from './ui/popover'

export type LatencyTests = ReturnType<typeof useLatencyTests>

export function LatencyStatus({ tests, scope }: { tests: LatencyTests; scope: string }) {
  const { t } = useTranslation()
  if (tests.scope !== scope) return null
  return (
    <>
      {tests.progress.total > 0 && (
        <div
          className="latency-status flex flex-wrap items-center justify-between gap-2 text-xs"
          role="status"
          aria-live="polite"
        >
          <div className="flex items-center gap-1">
            <span className="tabular-nums">
              {t(
                tests.busy ? 'latency.progress' : tests.remaining ? 'latency.paused' : 'latency.completed',
                tests.progress,
              )}
            </span>
            <Popover>
              <PopoverTrigger asChild>
                <Button size="icon" variant="ghost" className="size-8" aria-label={t('latency.measurementDetails')}>
                  <CircleHelp className="size-4" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="max-w-[calc(100vw-2rem)] text-xs">
                {t('latency.measurementHint')}
              </PopoverContent>
            </Popover>
          </div>
          <div className="flex flex-wrap gap-2">
            {tests.busy ? (
              <Button size="sm" variant="outline" onClick={tests.stop} disabled={tests.stopping}>
                {t(tests.stopping ? 'latency.stopping' : 'latency.stop')}
              </Button>
            ) : (
              <>
                {tests.remaining > 0 && (
                  <Button size="sm" variant="outline" onClick={tests.resume}>
                    {t('latency.resume', { count: tests.remaining })}
                  </Button>
                )}
                {tests.remaining === 0 && tests.failed > 0 && (
                  <Button size="sm" variant="ghost" onClick={tests.retryFailed}>
                    {t('latency.retryFailed', { count: tests.failed })}
                  </Button>
                )}
              </>
            )}
          </div>
        </div>
      )}
      {tests.error && (
        <p role="alert" className="text-sm text-destructive">
          {tests.error}
        </p>
      )}
    </>
  )
}
