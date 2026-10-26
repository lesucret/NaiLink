'use strict'
'require baseclass'

var colors = {
  trace: '#64748b',
  debug: '#7c3aed',
  info: '#0369a1',
  warning: '#b45309',
  error: '#dc2626',
  fatal: '#be123c',
  panic: '#be123c',
  plain: 'inherit',
}
return baseclass.extend({
  parse: function (text) {
    var previous = 'plain'
    return (text.match(/[^\n]*\n|[^\n]+$/g) || []).map(function (line) {
      var header =
        line.match(/^time=(?:"[^"]*"|\S+)\s+level=(\w+)\b/i) ||
        line.match(/^\[(trace|debug|info|warn|warning|error|fatal|panic)\]/i)
      var value = header && header[1].toLowerCase()
      if (line.charAt(0) === '{') {
        try {
          var level = JSON.parse(line).level
          if (typeof level === 'string') value = level.toLowerCase()
        } catch (_) {
          // A multiline JSON payload can continue an error record.
        }
      }
      if (value)
        previous = value === 'warn' ? 'warning' : Object.prototype.hasOwnProperty.call(colors, value) ? value : 'plain'
      return { text: line, level: previous }
    })
  },
  render: function (text) {
    return this.parse(text).map(function (entry) {
      return E(
        'span',
        {
          'data-log-level': entry.level,
          style: 'color:var(--nailink-log-' + entry.level + ',' + colors[entry.level] + ')',
        },
        entry.text,
      )
    })
  },
  legend: function () {
    return E('p', { style: 'display:flex;flex-wrap:wrap;gap:14px;font-size:12px', 'aria-label': '日志级别颜色说明' }, [
      E('span', { style: 'color:var(--nailink-log-debug,' + colors.debug + ')' }, 'DEBUG 调试'),
      E('span', { style: 'color:var(--nailink-log-info,' + colors.info + ')' }, 'INFO 信息'),
      E('span', { style: 'color:var(--nailink-log-warning,' + colors.warning + ')' }, 'WARN 警告'),
      E('span', { style: 'color:var(--nailink-log-error,' + colors.error + ')' }, 'ERROR 错误'),
    ])
  },
  theme: function (node) {
    if (!node || typeof window.getComputedStyle !== 'function') return
    var current = node,
      rgb = null
    while (current) {
      var background = window.getComputedStyle(current).backgroundColor
      if (background && background !== 'transparent' && background !== 'rgba(0, 0, 0, 0)') {
        rgb = background.match(/[\d.]+/g)
        break
      }
      current = current.parentElement
    }
    var dark = rgb && rgb.length >= 3 ? (Number(rgb[0]) + Number(rgb[1]) + Number(rgb[2])) / 3 < 128 : false
    var palette = dark
      ? {
          trace: '#94a3b8',
          debug: '#c4b5fd',
          info: '#7dd3fc',
          warning: '#fcd34d',
          error: '#fca5a5',
          fatal: '#fda4af',
          panic: '#fda4af',
          plain: 'inherit',
        }
      : colors
    Object.keys(palette).forEach(function (level) {
      node.style.setProperty('--nailink-log-' + level, palette[level])
    })
  },
})
