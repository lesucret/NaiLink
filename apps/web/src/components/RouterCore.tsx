import { useIsMutating, useQueryClient } from '@tanstack/react-query'
import { Cpu, LoaderCircle } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button } from '~/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '~/components/ui/dialog'
import { useRouterMaintenance } from '~/hooks/useRouterMaintenance'
import { coreProblemLabel, maintenanceMessages } from '~/utils/router-maintenance'

function problemLabel(value: string) {
  return (
    maintenanceMessages[value] ||
    (
      {
        unsupported_global_option: '有尚不能等价转换的全局选项（例如 MPTCP、TLS 分片）。',
        unsupported_dns_option: '当前 DNS 使用了尚未适配的响应规则、固定 TTL 或缓存容量设置。',
        unsupported_group_policy: '当前群组策略尚未适配。',
        unsupported_group_policy_parameter: '当前群组使用了尚未适配的策略参数。',
        invalid_fixed_group: '首版仅支持固定群组中只有一个节点；自动选择群组可包含多个节点。原群组保持不变。',
        unsupported_multi_wan: '当前配置指定了多个 WAN 接口，首版尚不能等价转换。',
        geodata_missing: '找不到当前配置引用的规则库。',
        missing_lan_interface: '请先配置并应用需要接管的 LAN 接口。',
        core_starting: '当前内核尚未就绪，请恢复服务后再检查。',
        unsupported_dns_fallback: '当前 DNS 回退策略尚未适配。',
      } as Record<string, string>
    )[value] ||
    coreProblemLabel(value)
  )
}
export function RouterCore() {
  const [open, setOpen] = useState(false)
  const [target, setTarget] = useState<'dae' | 'sing-box' | 'mihomo'>('sing-box')
  const { query, mutation, busy } = useRouterMaintenance()
  const pendingMutations = useIsMutating()
  const core = query.data?.cores
  const client = useQueryClient()
  const lastCore = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (!core?.current) return
    if (lastCore.current && lastCore.current !== core.current) client.invalidateQueries()
    lastCore.current = core.current
  }, [client, core?.current])
  const current =
    core?.current === 'sing-box' || core?.current === 'mihomo' || core?.current === 'dae' ? core.current : '未知'
  const plan = core?.plan?.target === target ? core.plan : undefined
  const selectedCore =
    target === 'dae'
      ? undefined
      : core?.engines?.[target] ||
        (target === 'sing-box' && core
          ? { installed: core.installed, supported: core.supported, version: core.version }
          : undefined)
  const repairCore = plan?.problems?.includes(
    target === 'mihomo' ? 'mihomo_version_mismatch' : 'singbox_version_mismatch',
  )
  const job = query.data?.job
  const coreJob = job?.action.startsWith('core_') ? job : undefined
  const suffix = target === 'sing-box' ? 'singbox' : target
  const ready = !!plan?.compatible && /^[a-f0-9]{64}$/i.test(plan.revision)
  const blocked =
    busy || pendingMutations > 0 || query.isError || !core || (target !== 'dae' && !selectedCore?.supported)
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="min-h-11"
        aria-label={`代理内核 ${current}，检查和切换`}
        onClick={() => {
          setTarget(current === 'sing-box' ? 'dae' : 'sing-box')
          setOpen(true)
          query.refetch()
        }}
      >
        <Cpu className="h-4 w-4" aria-hidden="true" />
        内核 {current}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[88dvh] overflow-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>代理内核</DialogTitle>
            <DialogDescription>
              保留节点、订阅和现有配置，仅切换当前运行配置。未应用的修改继续保留。切换会短暂断连，失败自动尝试恢复原内核，请勿断电。
            </DialogDescription>
          </DialogHeader>
          <p className="text-sm">
            当前内核：<strong>{current}</strong>
            {current !== 'dae' &&
              current !== '未知' &&
              core &&
              ` · ${core.engines?.[current]?.version || core.version}`}
          </p>
          {core?.code && (
            <p role="status" className="text-sm text-muted-foreground">
              {maintenanceMessages[core.code] || coreProblemLabel(core.code)}
            </p>
          )}
          <div className="grid grid-cols-3 gap-2" role="group" aria-label="目标内核">
            {(['dae', 'sing-box', 'mihomo'] as const).map((name) => (
              <Button
                key={name}
                variant={target === name ? 'default' : 'outline'}
                className="min-h-14 whitespace-normal px-2 text-xs sm:text-sm"
                aria-pressed={target === name}
                disabled={
                  busy ||
                  (name !== 'dae' &&
                    (core?.engines?.[name]?.supported ?? (name === 'sing-box' && core?.supported)) === false)
                }
                onClick={() => setTarget(name)}
              >
                {name}
                {name === current && ' · 当前'}
              </Button>
            ))}
          </div>
          <p className="text-xs leading-relaxed text-muted-foreground">
            dae 使用 eBPF 接管；sing-box、mihomo 按需安装固定版本，使用 TUN / nftables，支持 x86_64、ARM64。
            每次只运行一种内核，切换前检查实际配置和连接，失败尝试恢复原内核。不兼容参数会列出并阻止切换。
          </p>
          {target !== 'dae' && (!selectedCore?.installed || repairCore) && (
            <section className="space-y-2" aria-label="安装可选内核">
              <p className="text-xs text-muted-foreground">
                {target} · {selectedCore?.version || '—'}；按需下载，不使用时不运行。安装前请预留 180 MiB 可用存储。
              </p>
              <Button
                className="min-h-11 w-full whitespace-normal"
                disabled={blocked || current === target}
                onClick={() => mutation.mutate(target === 'mihomo' ? 'core_install_mihomo' : 'core_install_singbox')}
              >
                {repairCore ? `重新安装 ${target}（请先切换到其他内核）` : `下载并安装 ${target}（保留当前内核运行）`}
              </Button>
            </section>
          )}
          {target === 'sing-box' && (
            <section className="space-y-2 rounded-lg border p-3 text-sm" aria-label="扩展协议组件">
              <p className="font-medium">Naive · WireGuard · Tor</p>
              <p className="text-xs leading-relaxed text-muted-foreground">
                {core?.extra ? `扩展协议包已安装 · ${core.extra_version}` : '扩展协议包尚未安装'}。 仅 x86_64 / ARM64
                可用，下载约 30 MiB，请预留 400 MiB 存储。 不使用时不启动 Tor。Tor 仅支持 TCP，首次引导可能需要数分钟。
              </p>
              {!core?.extra && (
                <Button
                  variant="outline"
                  className="min-h-11 w-full whitespace-normal"
                  disabled={blocked || current === 'sing-box'}
                  onClick={() => mutation.mutate('core_install_extra')}
                >
                  {current === 'sing-box' ? '请先切回 dae 再安装扩展包' : '按需安装扩展协议包'}
                </Button>
              )}
            </section>
          )}
          {plan && (
            <section className="space-y-2 rounded-lg border p-3 text-sm" aria-label="内核兼容性报告">
              <p className="font-medium">
                {plan.compatible ? '兼容性检查通过' : '暂不能切换'} · {plan.nodes} 个运行节点 · {plan.groups} 个群组
              </p>
              {!!plan.problems?.length && (
                <ul className="space-y-1 break-words text-destructive">
                  {plan.problems.map((value, index) => (
                    <li key={index}>{problemLabel(value)}</li>
                  ))}
                </ul>
              )}
              {!!plan.warnings?.length && (
                <div className="space-y-1 break-words text-muted-foreground">
                  <p className="font-medium text-foreground">目标内核的行为差异</p>
                  {plan.warnings.map((value, index) => (
                    <p key={index}>{value}</p>
                  ))}
                </div>
              )}
            </section>
          )}
          {coreJob && (
            <div
              role={coreJob.state === 'error' ? 'alert' : 'status'}
              className={`rounded-lg border p-3 text-sm ${coreJob.state === 'error' ? 'text-destructive' : 'bg-muted/40'}`}
            >
              <p className="flex items-start gap-2">
                {busy && <LoaderCircle className="mt-0.5 h-4 w-4 shrink-0 animate-spin" aria-hidden="true" />}
                {coreJob.state === 'error'
                  ? maintenanceMessages[coreJob.code || ''] || maintenanceMessages.operation_failed
                  : coreJob.state === 'success'
                    ? coreJob.action.startsWith('core_switch_')
                      ? `切换完成，当前内核：${current}`
                      : coreJob.action === 'core_install_extra'
                        ? '扩展协议包已安装，现有节点和配置保留。'
                        : coreJob.action === 'core_install_singbox' || coreJob.action === 'core_install_mihomo'
                          ? '内核已安装，请检查兼容性后再切换。'
                          : '兼容性检查完成，请查看报告。'
                    : '正在检查、安装或切换；可关闭窗口，后台任务继续执行。'}
              </p>
            </div>
          )}
          {(query.isError || mutation.isError) && (
            <p role="alert" className="break-words text-sm text-destructive">
              {query.error?.message || mutation.error?.message}
            </p>
          )}
          {!busy && pendingMutations > 0 && (
            <p role="status" className="text-sm text-muted-foreground">
              请等待节点测试或其他保存操作完成后再切换。
            </p>
          )}
          <div className="grid gap-2 sm:grid-cols-3">
            <Button variant="outline" className="min-h-11" onClick={() => query.refetch()}>
              重新读取状态
            </Button>
            <Button
              variant="outline"
              className="min-h-11"
              disabled={blocked || (target !== 'dae' && !selectedCore?.installed)}
              onClick={() => mutation.mutate(`core_check_${suffix}`)}
            >
              检查兼容性
            </Button>
            <Button
              className="min-h-11"
              disabled={blocked || !ready || target === current}
              onClick={() => mutation.mutate(`core_switch_${suffix}`)}
            >
              切换到 {target}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}
