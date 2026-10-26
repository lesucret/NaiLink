'use strict'
'require view'
'require rpc'
'require poll'
'require ui'
'require dom'
'require view.daed-router.log-format as logFormat'
'require view.daed-router.community as community'

var logs = rpc.declare({ object: 'daed-router', method: 'logs', expect: {}, reject: true, raise: true })

return view.extend({
  handleSave: null,
  handleSaveApply: null,
  handleReset: null,
  render: function () {
    var following = true
    var latest = ''
    var pending = null
    var container = null
    var content = E('span', { style: 'display:block' }, '正在读取日志…')
    var output = E(
      'pre',
      {
        id: 'router-log-output',
        role: 'region',
        'aria-label': '运行日志内容',
        'aria-describedby': 'router-log-hint',
        tabindex: 0,
        style: 'white-space:pre-wrap;overflow-wrap:anywhere;max-height:65vh;overflow:auto;overflow-anchor:none',
        scroll: function () {
          var atBottom = output.scrollHeight - output.clientHeight - output.scrollTop <= 24
          if (following && !atBottom) setFollowing(false)
          else if (!following && atBottom) setFollowing(true)
        },
        keydown: function (event) {
          if (event.key === 'End' && !event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey) {
            event.preventDefault()
            setFollowing(true)
          } else if (['Home', 'PageUp', 'ArrowUp'].indexOf(event.key) !== -1) {
            setFollowing(false)
          }
        },
      },
      [content],
    )
    if (typeof ResizeObserver !== 'undefined') {
      var observer = new ResizeObserver(function () {
        if (following) output.scrollTop = output.scrollHeight
      })
      observer.observe(content)
      observer.observe(output)
    }
    var hint = E(
      'p',
      { id: 'router-log-hint', role: 'status', style: 'margin:4px 0;color:var(--text-color-medium,#666)' },
      '每 2 秒刷新 · 自动跟随最新日志',
    )
    var toggle = E(
      'button',
      {
        class: 'cbi-button',
        type: 'button',
        'aria-controls': 'router-log-output',
        style: 'min-height:44px',
        click: function () {
          setFollowing(!following)
        },
      },
      '暂停跟随',
    )
    var failure = E('p', { role: 'alert', style: 'display:none' })
    function showLatest() {
      var text = latest || '暂无日志。'
      if (content.textContent !== text) dom.content(content, logFormat.render(text))
      output.scrollTop = output.scrollHeight
    }
    function updateHint() {
      var text = following
        ? '每 2 秒刷新 · 自动跟随最新日志'
        : output.textContent !== (latest || '暂无日志。')
          ? '正在查看历史 · 有新日志，点击回到最新'
          : '正在查看历史 · 已暂停跟随'
      if (hint.textContent !== text) hint.textContent = text
      var label = following ? '暂停跟随' : '回到最新'
      if (toggle.textContent !== label) toggle.textContent = label
    }
    function setFollowing(value) {
      following = value
      if (following) {
        showLatest()
      }
      updateHint()
    }
    function refresh() {
      if (pending) return pending
      pending = logs()
        .then(function (data) {
          if (!data || typeof data.logs !== 'string') throw new Error('日志接口暂时不可用。')
          logFormat.theme(container)
          latest = data.logs
          failure.style.display = 'none'
          if (following) {
            showLatest()
          }
          updateHint()
        })
        .catch(function (error) {
          if (!latest && following) content.textContent = '暂无日志。'
          failure.textContent = '日志读取失败：' + error.message
          failure.style.display = ''
          hint.textContent = '更新失败，保留上次日志'
        })
        .finally(function () {
          pending = null
        })
      return pending
    }
    poll.add(refresh, 2)
    refresh()
    container = E('div', {}, [
      E(
        'div',
        {
          style:
            'display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px;margin-bottom:20px',
        },
        [E('h2', { style: 'margin:0' }, 'NaiLink 运行日志'), community.render()],
      ),
      E(
        'div',
        {
          style:
            'display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px;margin-bottom:12px',
        },
        [hint, toggle],
      ),
      failure,
      logFormat.legend(),
      output,
    ])
    return container
  },
})
