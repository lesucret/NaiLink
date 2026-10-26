const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const htdocs = path.join(__dirname, '../../openwrt/htdocs');
const menu = JSON.parse(fs.readFileSync(path.join(__dirname, '../../openwrt/files/menu.json')));
const panelModule = menu['admin/services/daed/dashboard'].action.path.split('/').pop();

function load(name, protocol = 'http:', page = 'dashboard') {
  const polls = [];
  const calls = [];
  const fetchCalls = [];
  let authorized = true;
  let rpcFailure = false;
  let session = '1234567890abcdef1234567890abcdef';
  let response = null;
  let networkFailure = false;
  let result = { running: true, listen: '127.0.0.1:2023', checks: [] };
  const context = vm.createContext({
    window: { location: { protocol, pathname: '/cgi-bin/luci/admin/services/daed/' + page } },
    L: { env: { sessionid: '1234567890abcdef1234567890abcdef' } },
    AbortController, setTimeout, clearTimeout,
    fetch: async (url, options) => { fetchCalls.push([url, options]); if (networkFailure) throw new Error('offline'); return response || {ok: authorized, status: authorized ? 204 : 401, headers: new Headers()}; },
    view: { extend: value => value },
    baseclass: { extend: value => value },
    rpc: { getSessionID: () => session, declare: spec => async (...args) => { calls.push([spec.method, ...args]); if (rpcFailure) throw new Error('read failed'); return result; } },
    poll: { add: (fn, interval) => { fn.interval = interval; polls.push(fn); } },
    ui: { createHandlerFn: (self, fn) => fn.bind(self), addNotification: () => {} },
    dom: { content: (node, content) => { node.children = content; } },
    E: (tag, attrs, children) => {
      const node = { tag, attrs, children, style: {}, scrollTop: 0, scrollHeight: 1000, clientHeight: 200 };
      Object.defineProperty(node,'textContent',{
        get(){ return (Array.isArray(this.children) ? this.children : [this.children]).map(child=>typeof child==='object' ? child?.textContent || '' : child || '').join(''); },
        set(value){ this.children = String(value); },
      });
      return node;
    },
  });
  vm.runInContext("String.prototype.format = function(...args) { let i=0; return this.replace(/%s/g, () => args[i++]); };", context);
  context.community = vm.runInContext('(function(){' + fs.readFileSync(path.join(htdocs, 'community.js'), 'utf8') + '\n})()', context);
  context.access = vm.runInContext('(function(){' + fs.readFileSync(path.join(htdocs, 'access.js'), 'utf8') + '\n})()', context);
  context.logFormat = vm.runInContext('(function(){' + fs.readFileSync(path.join(htdocs, 'log-format.js'), 'utf8') + '\n})()', context);
  context.maintenanceView = vm.runInContext('(function(){' + fs.readFileSync(path.join(htdocs, 'maintenance-view.js'), 'utf8') + '\n})()', context);
  const view = vm.runInContext('(function(){' + fs.readFileSync(path.join(htdocs, name + '.js'), 'utf8') + '\n})()', context);
  return { view, polls, calls, fetchCalls, setSession: value => {session = value;}, legacySession: () => {delete context.rpc.getSessionID;}, setResponse: value => {response = value;}, setNetworkFailure: () => {networkFailure = true;}, setAuthorized: value => { authorized = value; }, setResult: value => { result = value; }, setFailure: value => { rpcFailure = value; } };
}

function elements(node, tag) {
  if (!node || typeof node !== 'object') return [];
  return (node.tag === tag ? [node] : []).concat((Array.isArray(node.children) ? node.children : [node.children]).flatMap(child => elements(child, tag)));
}

test('all LuCI modules parse in the actual function-wrapper convention', () => {
  for (const file of fs.readdirSync(htdocs)) new Function(fs.readFileSync(path.join(htdocs, file), 'utf8'));
});

test('Daed menu exposes four ordered pages, defaulting to dashboard', () => {
  const entries = Object.entries(menu).filter(([key]) => key !== 'admin/services/daed').sort((a,b) => a[1].order - b[1].order);
  assert.deepEqual(entries.map(([key]) => key.split('/').pop()), ['dashboard', 'resources', 'advanced', 'logs']);
  assert.deepEqual(entries.map(([,entry]) => entry.title), ['管理面板','节点和订阅','高级设置','运行日志']);
});

test('native resource and advanced tabs embed their fixed same-origin route', () => {
  for (const page of ['resources', 'advanced']) {
    const { view } = load(panelModule, 'https:', page);
    assert.equal(elements(view.render({running:true,authorized:true}), 'iframe')[0].attrs.src, '/cgi-bin/daed-router/#/' + page);
  }
  const {view} = load(panelModule, 'https:', 'untrusted');
  assert.equal(elements(view.render({running:true,authorized:true}), 'iframe')[0].attrs.src, '/cgi-bin/daed-router/');
});

test('upgraded menu bypasses the cached pre-gateway dashboard and resolves every view', () => {
  assert.notEqual(menu['admin/services/daed/dashboard'].action.path, 'daed-router/dashboard');
  assert.notEqual(menu['admin/services/daed/dashboard'].action.path, 'daed-router/panel');
  assert.ok(fs.existsSync(path.join(htdocs,'panel.js')), 'Old cached menus still need a resolvable bootstrap view');
  for (const entry of Object.values(menu)) {
    if (entry.action.type !== 'view') continue;
    assert.equal(entry.action.path.split('/')[0], 'daed-router');
    assert.ok(fs.existsSync(path.join(htdocs, entry.action.path.split('/')[1] + '.js')));
  }
});

test('HTTP and HTTPS dashboards embed the authenticated same-origin gateway', () => {
  for (const protocol of ['http:', 'https:']) {
    const { view } = load(panelModule, protocol);
    const tree = view.render({ running: true, authorized: true });
    assert.equal(elements(tree, 'iframe')[0].attrs.src, '/cgi-bin/daed-router/');
    assert.equal(elements(tree, 'a').length, 0);
    assert.equal(elements(tree, 'h2').length, 0);
  }
});

test('stopped services and expired sessions never embed a bypass URL', () => {
  const { view } = load(panelModule);
  for (const data of [{ running: false }, { running: true, authorized: false }]) {
    assert.equal(elements(view.render(data), 'iframe').length, 0);
  }
});

test('an active update survives a full-page refresh while the proxy is stopped', () => {
  const module = load(panelModule);
  const tree = module.view.render({running:false, maintenance:{success:true,job:{action:'update',state:'running',phase:'installing'}}});
  assert.equal(elements(tree,'iframe').length,0);
  assert.ok(tree.textContent.includes('正在更新 NaiLink'));
  assert.equal(elements(tree,'button')[0].attrs.style,'min-height:44px');
  assert.equal(module.polls.length,1);
});

test('dashboard authorizes using the LuCI session in a same-origin POST body', async () => {
  const module = load(panelModule);
  const data = await module.view.load();
  assert.equal(data.authorized, true);
  const [url, options] = module.fetchCalls[0];
  assert.equal(url, '/cgi-bin/daed-router/session');
  assert.equal(options.credentials, 'same-origin');
  assert.equal(options.method, 'POST');
  assert.equal(JSON.parse(options.body).session, '1234567890abcdef1234567890abcdef');
});

test('rejected bootstrap cannot render the management iframe', async () => {
  const module = load(panelModule);
  module.setAuthorized(false);
  const data = await module.view.load();
  assert.equal(elements(module.view.render(data), 'iframe').length, 0);
});

for (const name of [panelModule, 'panel']) {
  test(name + ' follows the active RPC session, with legacy LuCI fallback', async () => {
    const module = load(name);
    const current = 'abcdef1234567890abcdef1234567890';
    module.setSession(current);
    assert.equal((await module.view.load()).authorized, true);
    assert.equal(JSON.parse(module.fetchCalls[0][1].body).session, current);
    assert.equal(module.fetchCalls[0][1].redirect, 'error');
    module.legacySession();
    await module.view.load();
    assert.equal(JSON.parse(module.fetchCalls[1][1].body).session, '1234567890abcdef1234567890abcdef');
  });

  test(name + ' refuses absent/anonymous RPC sessions without reverting to stale environment', async () => {
    for (const value of [undefined, '', 'bad', '0'.repeat(32)]) {
      const module = load(name);
      module.setSession(value);
      const data = await module.view.load();
      assert.equal(data.issue.code, 'session_missing');
      assert.equal(module.fetchCalls.length, 0);
      assert.equal(elements(module.view.render(data), 'iframe').length, 0);
    }
  });

  test(name + ' distinguishes authorization, firmware gateway and transport failures safely', async () => {
    for (const [status, code] of [[401,'session_expired'],[403,'gateway_denied'],[404,'gateway_missing'],[405,'gateway_missing'],[500,'gateway_unavailable'],[502,'gateway_unavailable'],[503,'gateway_unavailable'],[504,'rpc_timeout'],[200,'invalid_response']]) {
      const module = load(name);
      module.setResponse(new Response('login HTML, not an error to display', {status}));
      const data = await module.view.load();
      assert.equal(data.issue.code, code);
      const tree = module.view.render(data);
      assert.equal(elements(tree, 'iframe').length, 0);
      assert.match(tree.textContent, new RegExp(code));
      assert.doesNotMatch(tree.textContent, /login HTML/);
      assert.equal(elements(tree, 'button')[0].textContent, '重新加载');
    }
    for (const code of ['rpc_unavailable','origin_rejected','permission_denied','rpc_timeout']) {
      const module = load(name);
      module.setResponse(new Response(JSON.stringify({code}), {status:403,headers:{'Content-Type':'application/json'}}));
      assert.equal((await module.view.load()).issue.code, code);
    }
    const module = load(name);
    module.setNetworkFailure();
    assert.equal((await module.view.load()).issue.code, 'network_error');
    module.setFailure(true);
    assert.equal((await module.view.load()).issue.code, 'rpc_error');
    module.setFailure(false);
    for (const value of [6,{},null]) {
      module.setResult(value);
      const data = await module.view.load();
      assert.equal(data.issue.code, 'rpc_error');
      assert.equal(elements(module.view.render(data),'iframe').length,0);
    }
  });
}

test('logs are rendered as text instead of executable HTML', async () => {
  const module = load('logs');
  module.setResult({ logs: '<script>alert(1)</script>' });
  const tree = module.view.render();
  await module.polls[0]();
  assert.equal(elements(tree, 'pre')[0].textContent, '<script>alert(1)</script>');
});

test('native logs color record levels and continuations without altering text', async () => {
  const module=load('logs');
  const text='time="now" level=warning msg="failed"\ncontinuation\ntime="later" level=info msg="quoted level=error"\n{"level":"error","msg":"<script>"}\n';
  module.setResult({logs:text});const tree=module.view.render();await module.polls[0]();
  assert.equal(elements(tree,'pre')[0].textContent,text);
  const records=elements(tree,'span').filter(node=>node.attrs['data-log-level']);
  assert.deepEqual(records.map(node=>node.attrs['data-log-level']),['warning','warning','info','error']);
  assert.notEqual(records[0].attrs.style,records[2].attrs.style);
  assert.notEqual(records[2].attrs.style,records[3].attrs.style);
});

test('native logs follow the tail, freeze history and resume to the latest bounded snapshot', async () => {
  const module = load('logs');
  module.setResult({ logs: 'old snapshot' });
  const tree = module.view.render();
  const output = elements(tree, 'pre')[0];
  const button = elements(tree, 'button')[0];
  assert.equal(module.polls[0].interval, 2);
  await module.polls[0]();
  assert.equal(output.scrollTop, output.scrollHeight);
  output.scrollTop = 100;
  output.attrs.scroll();
  assert.equal(button.textContent, '回到最新');
  module.setResult({ logs: 'rotated tail: new snapshot' });
  await module.polls[0]();
  assert.equal(output.textContent, 'old snapshot');
  assert.equal(output.scrollTop, 100);
  button.attrs.click();
  assert.equal(output.textContent, 'rotated tail: new snapshot');
  assert.equal(output.scrollTop, output.scrollHeight);
  assert.equal(button.textContent, '暂停跟随');
  button.attrs.click();
  module.setResult({ logs: 'keyboard latest' });
  await module.polls[0]();
  let prevented = false;
  output.attrs.keydown({key:'End', preventDefault:()=>{prevented=true;}});
  assert.equal(prevented,true);
  assert.equal(output.textContent,'keyboard latest');
});

test('native log refresh errors preserve previous data and recover on the next successful poll', async () => {
  const module = load('logs');
  module.setResult({ logs:'last good snapshot' });
  const tree = module.view.render();
  await module.polls[0]();
  module.setFailure(true);
  await module.polls[0]();
  assert.equal(elements(tree,'pre')[0].textContent,'last good snapshot');
  const alert = elements(tree,'p').find(node=>node.attrs.role==='alert');
  assert.match(alert.textContent,/日志读取失败/);
  assert.equal(alert.style.display,'');
  module.setFailure(false);
  module.setResult({ logs:'recovered snapshot' });
  await module.polls[0]();
  assert.equal(elements(tree,'pre')[0].textContent,'recovered snapshot');
  assert.equal(alert.style.display,'none');
});

test('community links are reachable from logs and service fallback pages', () => {
  for (const name of ['logs', panelModule, 'panel']) {
    const module = load(name);
    const tree = name === 'logs' ? module.view.render() : module.view.render({running:false});
    const links = elements(tree, 'a');
    assert.deepEqual(links.map(link => link.attrs.href), ['https://www.youtube.com/@naiyou', 'https://t.me/naiyous']);
    for (const link of links) {
      assert.equal(link.attrs.target, '_blank');
      assert.equal(link.attrs.rel, 'noopener noreferrer');
      assert.ok(link.attrs['aria-label']);
    }
  }
});
