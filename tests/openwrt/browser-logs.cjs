/* Synthetic log file in native_panel's disposable rootfs; real LuCI RPC and scroll UI. */
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const { chromium } = require('playwright')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
const controlDirectory = process.env.DAED_PANEL_CONTROL
const output = process.env.DAED_LOG_OUTPUT || '.build/logs-browser'
if (!controlDirectory) throw new Error('Set DAED_PANEL_CONTROL to the disposable native_panel fixture')
fs.mkdirSync(output, { recursive: true })
async function control(action, extra = {}) {
  const id = `${Date.now()}-${Math.random()}`
  fs.writeFileSync(path.join(controlDirectory, 'command.json'), JSON.stringify({ id, action, ...extra }))
  for (let i = 0; i < 200; i++) {
    try {
      const result = JSON.parse(fs.readFileSync(path.join(controlDirectory, 'result.json'), 'utf8'))
      if (result.id === id) return result
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  throw new Error(`Fixture action timed out: ${action}`)
}
function records(last) {
  return Array.from(
    { length: 140 },
    (_, i) => `time="Oct 20 21:22:00" level=info msg="Log-${last - 139 + i} · ${'连接日志 '.repeat(12)}"`,
  ).join('\n')
}
function isLogsRequest(request) {
  if (request.method() !== 'POST') return false
  try {
    const body = request.postDataJSON()
    return (Array.isArray(body) ? body : [body]).some(
      (item) => item?.method === 'call' && item.params?.[1] === 'daed-router' && item.params?.[2] === 'logs',
    )
  } catch {
    return false
  }
}
async function position(pre) {
  return pre.evaluate((el) => ({ top: el.scrollTop, height: el.scrollHeight, viewport: el.clientHeight }))
}
async function bottom(pre) {
  for (let i = 0; i < 30; i++) {
    const p = await position(pre)
    if (p.height - p.viewport - p.top <= 2) break
    await new Promise((resolve) => setTimeout(resolve, 30))
  }
  const p = await position(pre)
  assert.ok(p.height - p.viewport - p.top <= 2, JSON.stringify(p))
}
async function login(page) {
  await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
  if (await page.locator('input[name=luci_username]').count()) {
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).click()
  }
  await page.frameLocator('iframe').getByRole('button', { name: '主题', exact: true }).waitFor({ timeout: 45000 })
}
async function dragHistory(page, pre) {
  const box = await pre.boundingBox()
  const x = box.x + box.width / 2
  const end = Math.min(box.y + box.height - 30, 900)
  const start = Math.max(box.y + 30, end - 180)
  const session = await page.context().newCDPSession(page)
  await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: start }] })
  for (let i = 1; i <= 12; i++) {
    await session.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [{ x, y: start + ((end - start) * i) / 12 }],
    })
    await page.waitForTimeout(20)
  }
  await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
  await session.detach()
}
async function exercise(page, root, width, name, calls) {
  const pre = root.getByRole('region', { name: '运行日志内容', exact: true })
  await pre.filter({ hasText: /Log-200/ }).waitFor()
  await pre.evaluate(async () => {
    await document.fonts.ready
  })
  await bottom(pre)
  // Fonts or viewport changes can increase scrollHeight without a new RPC.
  const content = pre.locator(':scope > span')
  await content.evaluate((el) => {
    el.style.lineHeight = '1.9'
  })
  await bottom(pre)
  await content.evaluate((el) => {
    el.style.lineHeight = ''
  })
  await bottom(pre)
  assert.equal(await pre.getAttribute('tabindex'), '0')
  const startCalls = calls()
  await control('logs', { text: records(201) })
  await pre.filter({ hasText: /Log-201/ }).waitFor({ timeout: 4000 })
  await bottom(pre)
  assert.ok(calls() > startCalls, 'log RPC did not refresh')
  if (width < 500) {
    await dragHistory(page, pre)
    await root.getByRole('button', { name: '回到最新', exact: true }).waitFor()
  }

  // Keyboard scrolling exercises native scroll events (no test-only component hooks).
  await pre.focus()
  await pre.press('Home')
  await root.getByRole('button', { name: '回到最新', exact: true }).waitFor()
  for (let i = 0; i < 40 && (await position(pre)).top > 0; i++) await page.waitForTimeout(30)
  assert.equal((await position(pre)).top, 0, 'Home did not finish scrolling')
  const frozen = await pre.innerText()
  const oldTop = (await position(pre)).top
  const beforePoll = calls()
  await control('logs', { text: records(240) })
  await root
    .getByRole('status')
    .filter({ hasText: /有新日志/ })
    .waitFor({ timeout: 4500 })
  assert.ok(calls() > beforePoll, 'paused view must keep collecting new snapshots')
  assert.equal(await pre.innerText(), frozen, 'rotating server tail changed the history being read')
  assert.equal((await position(pre)).top, oldTop, 'new data moved historical scroll position')
  await content.evaluate((el) => {
    el.style.lineHeight = '1.9'
  })
  await page.waitForTimeout(60)
  assert.equal((await position(pre)).top, oldTop, 'reflow forced historical view to the tail')
  await content.evaluate((el) => {
    el.style.lineHeight = ''
  })
  if (width < 500) await root.getByRole('button', { name: '回到最新', exact: true }).tap()
  else await root.getByRole('button', { name: '回到最新', exact: true }).click()
  await pre.filter({ hasText: /Log-240/ }).waitFor()
  await bottom(pre)

  // Explicit pause works even while already at the bottom; End resumes after scrolling up.
  await root.getByRole('button', { name: '暂停跟随', exact: true }).click()
  await control('logs', { text: records(250) })
  await root
    .getByRole('status')
    .filter({ hasText: /有新日志/ })
    .waitFor({ timeout: 4500 })
  assert.ok(!(await pre.innerText()).includes('Log-250'))
  await pre.focus()
  await pre.press('Home')
  for (let i = 0; i < 40 && (await position(pre)).top > 0; i++) await page.waitForTimeout(30)
  await pre.press('End')
  try {
    await pre.filter({ hasText: /Log-250/ }).waitFor({ timeout: 4000 })
  } catch (error) {
    console.error(
      name,
      width,
      await position(pre),
      await root.getByRole('status').allTextContents(),
      await pre.evaluate((el) => ({ active: document.activeElement?.id, tail: el.textContent.slice(-160) })),
    )
    throw error
  }
  await bottom(pre)
  // Real LuCI RPC may reject a nonzero status or coerce a missing payload to {}.
  // Neither may replace a successful snapshot; the next valid poll must recover.
  for (const result of [[3], [0, {}]]) {
    const snapshot = await pre.innerText()
    let injected = false
    const failLogs = async (route) => {
      if (!isLogsRequest(route.request())) return route.continue()
      const request = route.request().postDataJSON()
      const ids = (Array.isArray(request) ? request : [request])
        .filter((item) => item?.params?.[1] === 'daed-router' && item.params?.[2] === 'logs')
        .map((item) => item.id)
      const response = await route.fetch()
      const body = await response.json()
      const replace = (item) => (ids.includes(item.id) ? { jsonrpc: '2.0', id: item.id, result } : item)
      injected = true
      return route.fulfill({ response, json: Array.isArray(body) ? body.map(replace) : replace(body) })
    }
    await page.route('**/*', failLogs)
    try {
      await root.getByRole('alert').filter({ hasText: /日志读取失败/ }).waitFor({ timeout: 6000 })
      assert.ok(injected, 'RPC fault was not injected')
      assert.equal(await pre.innerText(), snapshot, 'failed RPC erased the last log snapshot')
    } finally {
      await page.unroute('**/*', failLogs)
    }
    await root.getByRole('alert').filter({ hasText: /日志读取失败/ }).waitFor({ state: 'hidden', timeout: 6000 })
  }
  const button = root.getByRole('button', { name: '暂停跟随', exact: true })
  assert.ok((await button.boundingBox()).height >= 44, 'touch control is too small')
  assert.ok(await pre.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'log has horizontal overflow')
  await page.screenshot({ path: path.join(output, `${name}-${width}.png`), fullPage: true })
  console.log(
    `PASS ${name} ${width}: tail/history, pause/resume, RPC failure/recovery, keyboard and touch target`,
  )
}
;(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  try {
    for (const width of [1600, 390]) {
      const context = await browser.newContext({
        viewport: { width, height: 1000 },
        locale: 'zh-CN',
        ...(width < 500 ? { isMobile: true, hasTouch: true } : {}),
      })
      const page = await context.newPage()
      if (process.env.DAED_LOG_PREVIEW_DIST) {
        const dist = path.resolve(process.env.DAED_LOG_PREVIEW_DIST)
        await page.route('**/cgi-bin/daed-router/**', (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          const file = path.resolve(
            dist,
            new URL(route.request().url()).pathname.split('/cgi-bin/daed-router/')[1] || 'index.html',
          )
          if (!file.startsWith(dist + path.sep) || !fs.existsSync(file)) return route.continue()
          return route.fulfill({ path: file })
        })
      }
      const errors = []
      let logCalls = 0
      page.on('pageerror', (e) => errors.push(e.message))
      page.on('request', (request) => {
        if (isLogsRequest(request)) logCalls++
      })
      await control('logs', { text: records(200) })
      await login(page)
      await page.locator('a[href$="/daed/logs"]:visible').first().click()
      const frame = page.frameLocator('iframe')
      await frame.getByRole('heading', { name: 'NaiLink 运行日志', exact: true }).waitFor()
      await exercise(page, frame, width, 'workspace', () => logCalls)
      const beforeNavigation = logCalls
      await page.locator('a[href$="/daed/dashboard"]:visible').first().click()
      await frame.locator('[data-workspace="dashboard"]').waitFor()
      await page.waitForTimeout(2600)
      assert.equal(logCalls, beforeNavigation, 'log polling continued after leaving the page')

      // A direct LuCI log URL uses the standalone native fallback, not the iframe route.
      await control('logs', { text: records(200) })
      await page.goto(`${base}/cgi-bin/luci/admin/services/daed/logs`)
      await page.getByRole('heading', { name: 'NaiLink 运行日志', exact: true }).waitFor()
      assert.equal(await page.locator('iframe').count(), 0)
      await exercise(page, page, width, 'native', () => logCalls)
      assert.deepEqual(errors, [])
      await context.close()
    }
  } finally {
    await browser.close()
  }
})().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
