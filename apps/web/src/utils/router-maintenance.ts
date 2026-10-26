export interface MaintenanceJob {
  id: string
  action: 'check' | 'update' | 'geodata'
  state: 'queued' | 'running' | 'success' | 'error'
  phase: string
  code?: string
  target?: string
  started: number
  updated: number
}
export interface MaintenanceStatus {
  success: boolean
  code?: string
  current: string
  latest?: { version: string; tag: string; checked: number }
  geodata?: { version: string; tag: string; checked: number }
  job?: MaintenanceJob
}

export const maintenanceMessages: Record<string, string> = {
  busy: '已有维护或安装任务正在执行，请稍后再试。',
  check_required: '请先检查最新版本，再开始更新。',
  already_latest: '当前已是最新版本。',
  permission_denied: '当前账户没有维护权限，请使用管理员账户重新登录。',
  worker_unavailable: '维护任务未能启动，请检查插件安装和 procd 服务。',
  download_failed: '下载失败，请检查路由器自身的 DNS、HTTPS 连接和系统时间。原有数据保留。',
  invalid_release: '没有找到完整、可用的发布版本，请稍后重试。',
  checksum_failed: '文件校验失败，已停止更新。',
  invalid_installer: '安装脚本版本或格式异常，已停止更新。',
  install_failed: '安装未完成，节点和配置备份已保留，请查看运行日志并按安装文档恢复。',
  update_rolled_back: '更新未完成，已恢复原安装及服务状态；节点和配置备份保留。请检查日志后再试。',
  install_missing: '现有插件已被移除，在线更新已停止，没有重新安装。',
  version_changed: '安装期间版本已发生变化，请重新检查更新。',
  unsupported: '当前固件、架构或内核未通过兼容性检查，未强制安装。',
  no_space: '可用存储空间不足，请清理临时文件或备份后重试。',
  backup_failed: '配置备份失败，已停止更新。',
  service_not_ready: '新版本管理服务未就绪，请查看运行日志；现有节点和备份保留。',
  custom_geodata: '检测到自定义 GeoIP/GeoSite，已保留原文件。本入口仅更新 NaiLink 独立管理的规则库。',
  invalid_geodata: '规则库格式或内容校验失败，未替换旧版。',
  configuration_modified: '当前有未应用配置，请先应用或恢复这些配置，再更新规则库。',
  apply_failed: '新规则库加载失败，已恢复旧版。',
  apply_unconfirmed: '规则库加载结果尚未确认，两份数据均已保留。请稍后查看状态或运行日志，勿重复更新。',
  rollback_failed: '自动回退未完成，服务已停止，原配置备份保留。请按安装文档修复，避免卸载清除数据。',
  backend_unavailable: '无法读取或重新加载代理配置，请确认服务正常后重试。',
  unsafe_state: '维护目录状态异常，已停止操作以保护现有文件。',
  state_failed: '无法保存维护状态，请检查存储空间和文件权限。',
  interrupted: '维护任务被中断，请重新检查当前版本与运行日志后再重试。',
  operation_failed: '维护操作未完成，请查看运行日志后重试。',
}

export function jobActive(job?: MaintenanceJob) {
  return job?.state === 'queued' || job?.state === 'running'
}

export class MaintenanceAccessError extends Error {}
export function maintenanceAccessExpired(error: unknown) {
  return error instanceof MaintenanceAccessError
}
export function newerVersion(next: string, current: string) {
  const a = next.split('.').map(Number)
  const b = current.split('-')[0].split('.').map(Number)
  if (a.length !== 3 || b.length !== 3 || [...a, ...b].some((value) => !Number.isFinite(value))) return false
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i]
  }
  return false
}

async function maintenanceRPC(method: 'maintenance' | 'maintain', action?: string): Promise<MaintenanceStatus> {
  const host = window.parent as Window & {
    L?: {
      require: (
        name: string,
      ) => Promise<{ declare: (spec: object) => (...args: string[]) => Promise<MaintenanceStatus> }>
    }
  }
  if (host === window || !host.L?.require) throw new Error('请从路由器后台进入维护功能。')
  const rpc = await host.L.require('rpc')
  let result: MaintenanceStatus
  try {
    result = await rpc.declare({
      object: 'daed-router',
      method,
      params: method === 'maintain' ? ['action'] : [],
      expect: {},
      raise: true,
      reject: true,
    })(...(action ? [action] : []))
  } catch (error) {
    const failure = error as { code?: number; name?: string; message?: string }
    if (
      failure?.code === 6 ||
      failure?.name === 'SessionError' ||
      (failure?.name === 'RPCError' &&
        /\b(?:ubus code 6|error -32002|HTTP error (?:401|403))\b/.test(failure.message || ''))
    )
      throw new MaintenanceAccessError('LuCI 登录已失效或没有维护权限，请重新登录后台。')
    throw new Error('维护接口暂时不可用，请稍后重试；持续失败时重新登录后台。')
  }
  if (!result || typeof result.success !== 'boolean') throw new Error('维护接口不可用，请更新插件并重新登录后台。')
  if (!result.success) throw new Error(maintenanceMessages[result.code || ''] || maintenanceMessages.operation_failed)
  return result
}
export const readMaintenance = () => maintenanceRPC('maintenance')
export const startMaintenance = (action: 'check' | 'update' | 'geodata') => maintenanceRPC('maintain', action)
