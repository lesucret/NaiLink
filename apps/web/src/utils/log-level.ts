export type LogLevel = 'trace' | 'debug' | 'info' | 'warning' | 'error' | 'fatal' | 'panic' | 'plain'

export function splitLogLevels(text: string): { text: string; level: LogLevel }[] {
  let previous: LogLevel = 'plain'
  return (text.match(/[^\n]*\n|[^\n]+$/g) || []).map((line) => {
    // Only the record header determines severity; messages may quote errors.
    const header =
      line.match(/^time=(?:"[^"]*"|\S+)\s+level=(\w+)\b/i) ||
      line.match(/^\[(trace|debug|info|warn|warning|error|fatal|panic)\]/i)
    let value = header?.[1]?.toLowerCase()
    if (line.startsWith('{')) {
      try {
        const level = JSON.parse(line).level
        if (typeof level === 'string') value = level.toLowerCase()
      } catch {
        // A multiline payload beginning with "{" is still a continuation.
      }
    }
    if (value)
      previous =
        value === 'warn'
          ? 'warning'
          : ['trace', 'debug', 'info', 'warning', 'error', 'fatal', 'panic'].includes(value)
            ? (value as LogLevel)
            : 'plain'
    return { text: line, level: previous }
  })
}

export const logLevelColors: Record<LogLevel, string> = {
  trace: 'text-slate-500 dark:text-slate-400',
  debug: 'text-violet-700 dark:text-violet-300',
  info: 'text-sky-800 dark:text-sky-300',
  warning: 'text-amber-800 dark:text-amber-300',
  error: 'text-red-700 dark:text-red-300',
  fatal: 'font-semibold text-rose-800 dark:text-rose-300',
  panic: 'font-semibold text-rose-800 dark:text-rose-300',
  plain: 'text-foreground',
}
