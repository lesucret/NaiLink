import { Download, LoaderCircle, RefreshCw } from 'lucide-react'
import { useRef, useState } from 'react'
import { Button } from '~/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '~/components/ui/dialog'
import { useRouterMaintenance } from '~/hooks/useRouterMaintenance'
import { jobActive, maintenanceMessages, newerVersion } from '~/utils/router-maintenance'

function MaintenanceProgress({ type }: { type: 'update' | 'geodata' }) {
  const { query, mutation } = useRouterMaintenance()
  const job = query.data?.job
  if (!job || (type === 'geodata' ? job.action !== 'geodata' : job.action === 'geodata')) return null
  const phases: Record<string, string> = {
    queued: '任务已排队',
    checking: '正在检查发布版本与配置',
    downloading: '正在下载更新文件',
    verifying: '正在校验文件',
    installing: '正在备份、安装和恢复服务，请勿断电',
    applying: '正在加载规则库',
    finished: '已完成',
  }
  return (
    <div
      role={job.state === 'error' ? 'alert' : 'status'}
      className={`rounded-lg border p-3 text-sm ${job.state === 'error' ? 'border-destructive/30 text-destructive' : 'bg-muted/40'}`}
    >
      <p className="flex items-center gap-2">
        {jobActive(job) && <LoaderCircle className="h-4 w-4 animate-spin" aria-hidden="true" />}
        {job.state === 'error'
          ? maintenanceMessages[job.code || ''] || maintenanceMessages.operation_failed
          : job.state === 'success'
            ? job.action === 'check'
              ? '版本检查完成'
              : type === 'geodata'
                ? job.code === 'already_latest'
                  ? '规则库已是最新，无需重新加载'
                  : '规则库已更新'
                : '插件更新完成'
            : phases[job.phase] || '维护任务正在执行'}
      </p>
      {job.state === 'success' && job.action === 'update' && (
        <Button size="sm" className="mt-3 min-h-11" onClick={() => window.parent.location.reload()}>
          重新加载新版本
        </Button>
      )}
      {mutation.isError && <p className="mt-1">{mutation.error.message}</p>}
    </div>
  )
}

function MaintenanceConnection() {
  const { query } = useRouterMaintenance()
  if (!query.isError) return null
  return (
    <div role="alert" className="mt-3 rounded-lg border border-destructive/30 p-3 text-sm text-destructive">
      <p>{query.error.message}</p>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button variant="outline" className="min-h-11" onClick={() => query.refetch()}>
          重新读取状态
        </Button>
        <Button variant="outline" className="min-h-11" onClick={() => window.parent.location.reload()}>
          重新加载后台
        </Button>
      </div>
    </div>
  )
}

export function RouterVersion() {
  const [open, setOpen] = useState(false)
  const { query, mutation, busy } = useRouterMaintenance()
  const data = query.data
  const loadedVersion = useRef<string | undefined>(undefined)
  const latest = data?.latest
  const available = !!latest && newerVersion(latest.version, data?.current || '')
  const show = () => {
    setOpen(true)
    loadedVersion.current ??= data?.current
    const justUpdated =
      data?.job?.action === 'update' && data.job.state === 'success' && data.current !== loadedVersion.current
    if (!busy && !justUpdated && !latest && data?.job?.state !== 'error') mutation.mutate('check')
  }
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="min-h-11 tabular-nums"
        onClick={show}
        aria-label={`NaiLink 版本 ${data?.current || '读取中'}，检查更新`}
      >
        <RefreshCw className={`h-3.5 w-3.5 ${busy ? 'animate-spin' : ''}`} aria-hidden="true" />
        {data?.current ? `v${data.current}` : '版本'}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[85dvh] overflow-auto">
          <DialogHeader>
            <DialogTitle>NaiLink 在线更新</DialogTitle>
            <DialogDescription>
              保留节点、订阅、群组和配置。安装时会备份数据并重启服务，连接可能短暂中断。
            </DialogDescription>
          </DialogHeader>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted-foreground">当前版本</dt>
            <dd>{data?.current || '正在读取'}</dd>
            <dt className="text-muted-foreground">最新发布</dt>
            <dd>{latest ? `v${latest.version}` : '尚未检查'}</dd>
          </dl>
          <MaintenanceProgress type="update" />
          <MaintenanceConnection />
          {mutation.isError && (
            <p role="alert" className="text-sm text-destructive">
              {mutation.error.message}
            </p>
          )}
          {!busy && query.data?.job?.state !== 'error' && latest && !available && (
            <p className="text-sm text-muted-foreground">当前已是最新发布版本。</p>
          )}
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="outline" className="min-h-11" disabled={busy} onClick={() => mutation.mutate('check')}>
              检查更新
            </Button>
            <Button
              className="min-h-11"
              disabled={busy || query.isError || !available}
              onClick={() => mutation.mutate('update')}
            >
              <Download aria-hidden="true" />
              更新到 {latest?.version || '最新版本'}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}

export function RouterGeodata() {
  const { query, mutation, busy } = useRouterMaintenance()
  const data = query.data?.geodata
  return (
    <section className="col-span-full rounded-xl border bg-card p-5" aria-label="GeoIP 和 GeoSite 规则库">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h3 className="text-base font-semibold">GeoIP / GeoSite 规则库</h3>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            使用 NaiLink 独立目录，不修改其他插件的规则库。两份文件一起校验更新；代理运行时重新加载，失败恢复旧版。
          </p>
          <p className="mt-2 text-xs text-muted-foreground">
            来源：Loyalsoldier/v2ray-rules-dat · {data ? `当前批次 ${data.version}` : '当前使用系统规则库或自定义文件'}
          </p>
        </div>
        <Button className="min-h-11" disabled={busy || query.isError} onClick={() => mutation.mutate('geodata')}>
          <RefreshCw className={busy ? 'animate-spin' : ''} aria-hidden="true" />
          一键更新规则库
        </Button>
      </div>
      <div className="mt-3">
        <MaintenanceProgress type="geodata" />
        <MaintenanceConnection />
      </div>
      {mutation.isError && (
        <p role="alert" className="mt-3 text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}
    </section>
  )
}
