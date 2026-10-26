'use strict'
'require baseclass'
'require rpc'
'require poll'
'require dom'

var read = rpc.declare({ object: 'daed-router', method: 'maintenance', expect: {}, raise: true, reject: true })
function active(data) {
  return (
    data && data.job && data.job.action === 'update' && (data.job.state === 'queued' || data.job.state === 'running')
  )
}
function load() {
  return read()
    .then(function (data) {
      return data && data.success === true ? data : null
    })
    .catch(function () {
      return null
    })
}
return baseclass.extend({
  load: load,
  render: function (data) {
    if (data && data.job && data.job.code === 'rollback_failed')
      return E('section', { role: 'alert', style: 'max-width:48em;padding:20px;overflow-wrap:anywhere' }, [
        E('h2', {}, '自动回退未完成'),
        E('p', {}, '原配置备份保存在 /root/daed-backups。请通过 SSH 安装菜单修复并查看运行日志；避免卸载清除数据。'),
        E(
          'button',
          {
            class: 'cbi-button',
            style: 'min-height:44px',
            click: function () {
              window.location.reload()
            },
          },
          '重新读取状态',
        ),
      ])
    if (!active(data)) return null
    var phases = {
      queued: '等待后台任务启动',
      checking: '检查发布版本',
      downloading: '下载更新文件',
      verifying: '校验更新文件',
      installing: '备份、安装和恢复服务',
    }
    var hint = E('p', { role: 'status', 'aria-live': 'polite' }, phases[data.job.phase] || '正在读取更新进度')
    poll.add(function () {
      return load().then(function (next) {
        if (!next || !active(next)) {
          window.location.reload()
          return
        }
        dom.content(hint, phases[next.job.phase] || '正在读取更新进度')
      })
    }, 2)
    return E(
      'section',
      { 'aria-label': 'NaiLink 更新进度', style: 'max-width:48em;padding:20px;overflow-wrap:anywhere' },
      [
        E('h2', {}, '正在更新 NaiLink'),
        hint,
        E('p', {}, '更新任务在路由器后台执行，连接可能短暂中断。请勿断电；节点和配置会先备份。完成后自动重新加载。'),
        E(
          'button',
          {
            class: 'cbi-button',
            style: 'min-height:44px',
            click: function () {
              window.location.reload()
            },
          },
          '重新读取进度',
        ),
      ],
    )
  },
})
