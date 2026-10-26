/* Real LuCI/rpcd/procd, static sing-box binary, API-only core sessions.
 * Transparent networking is independently exercised by core_netns.py. */
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const process = require('node:process')
const { chromium } = require('playwright')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
const directory = process.env.DAED_PANEL_CONTROL
const output = process.env.DAED_CORE_OUTPUT || '.build/core-browser'
if (!directory) throw new Error('Set DAED_PANEL_CONTROL')
fs.mkdirSync(output, { recursive: true })
async function control(action, extra = {}) {
  const id = `${Date.now()}-${Math.random()}`
  fs.writeFileSync(path.join(directory, 'command.json'), JSON.stringify({ id, action, ...extra }))
  for (let i = 0; i < 300; i++) {
    try {
      const r = JSON.parse(fs.readFileSync(path.join(directory, 'result.json')))
      if (r.id === id) return r
    } catch {}
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`Control timeout ${action}`)
}
async function waitText(locator, pattern) {
  for (let i = 0; i < 250; i++) {
    if (pattern.test(await locator.textContent())) return
    await new Promise((r) => setTimeout(r, 200))
  }
  assert.match(await locator.textContent(), pattern)
}
;(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  let page
  try {
    const errors = []
    const context = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
    page = await context.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).click()
    let frame = page.frameLocator('iframe')
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    const gql = async (query) => {
      const r = await context.request.post(`${base}/cgi-bin/daed-router/graphql`, {
        headers: { Origin: base },
        data: { query },
      })
      assert.equal(r.status(), 200)
      const v = await r.json()
      assert.ok(!v.errors, JSON.stringify(v.errors))
      return v.data
    }
    const config = (
      await gql(
        'mutation{createConfig(name:"Core UI fixture",global:{checkInterval:"30s",tcpCheckUrl:["http://127.0.0.1:18081/"]}){id}}',
      )
    ).createConfig.id
    const dns = (
      await gql(
        'mutation{createDns(name:"Core fixture",dns:"upstream { test: \'udp://127.0.0.1:53\' } routing { request { fallback: test } }"){id}}',
      )
    ).createDns.id
    const route = (await gql('mutation{createRouting(name:"Core fixture",routing:"fallback: direct"){id}}'))
      .createRouting.id
    await gql(
      `mutation{selectConfig(id:${JSON.stringify(config)}) selectDns(id:${JSON.stringify(dns)}) selectRouting(id:${JSON.stringify(route)}) run(dry:false)}`,
    )
    await gql(
      'mutation{importNodes(rollbackError:true,args:[{tag:"Preserved node",link:"socks5://127.0.0.1:19092#preserved"}]){error node{id}}}',
    )
    await gql(`mutation{updateConfig(id:${JSON.stringify(config)},global:{checkInterval:"31s"}){id}}`)
    await control('snapshot')
    await frame.getByRole('button', { name: /代理内核 dae/ }).click()
    let dialog = frame.getByRole('dialog')
    await control('maintenance', { fail: 'download' })
    await dialog.getByRole('button', { name: /下载并安装 sing-box/ }).click()
    await waitText(dialog, /下载失败/)
    await control('maintenance', { fail: '' })
    await dialog.getByRole('button', { name: /下载并安装 sing-box/ }).click()
    await waitText(dialog, /内核已安装/)
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 sing-box' }).click()
    await waitText(dialog, /切换完成，当前内核：sing-box/)
    assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).current, 'sing-box')
    await page.screenshot({ path: path.join(output, 'desktop-singbox.png'), fullPage: true })
    await dialog.getByRole('button', { name: 'dae', exact: true }).click()
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 dae' }).click()
    await waitText(dialog, /切换完成，当前内核：dae/)
    await control('core_fault', { fail: true })
    await dialog.getByRole('button', { name: 'sing-box', exact: true }).click()
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 sing-box' }).click()
    await waitText(dialog, /已恢复原内核/)
    assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).current, 'dae')
    await control('core_fault', { fail: false })
    assert.equal((await gql('{general{dae{modified}}}')).general.dae.modified, true)
    assert.equal((await control('compare')).unchanged, true)
    await context.close()
    const mobile = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      locale: 'zh-CN',
    })
    page = await mobile.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).tap()
    frame = page.frameLocator('iframe')
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    await frame.getByRole('button', { name: /代理内核 dae/ }).tap()
    dialog = frame.getByRole('dialog')
    await dialog.getByRole('button', { name: '检查兼容性' }).tap()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 sing-box' }).tap()
    await waitText(dialog, /切换完成，当前内核：sing-box/)
    const box = await dialog.boundingBox()
    assert.ok(box.width <= 390 && box.x >= 0, JSON.stringify(box))
    await page.screenshot({ path: path.join(output, 'mobile-singbox.png'), fullPage: true })
    await page.reload()
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    await frame.getByRole('button', { name: /代理内核 sing-box/ }).tap()
    await waitText(frame.getByRole('dialog'), /当前内核：sing-box/)
    assert.equal((await control('compare')).unchanged, true)
    assert.deepEqual(errors, [])
    const result = {
      realLuCI: true,
      realRPC: true,
      realProcd: true,
      officialStaticInstall: true,
      downloadFailureRetry: true,
      daeSingboxDae: true,
      lateFailureRollback: true,
      allUserTablesPreserved: true,
      pendingEditsPreserved: true,
      mobileTouchSwitch: true,
      mobileRefreshRecovery: true,
      noHorizontalDialogOverflow: true,
      apiOnly: true,
    }
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(result, null, 2))
    process.stdout.write(`${JSON.stringify(result)}\n`)
    await mobile.close()
  } catch (error) {
    if (page) await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true }).catch(() => {})
    throw error
  } finally {
    await browser.close()
  }
})().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
