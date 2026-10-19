import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { compile } from 'svelte/compiler';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createServer } from 'vite';

// Only this component and synthetic responses are mounted. All IPC is mocked;
// non-local network requests are blocked and no actual credentials are read.
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const officialProfile = {
  id: 'billing-test-claude', name: 'Claude 官方', protocol: 'anthropic_messages',
  base_url: 'https://api.anthropic.com/v1', enabled: true, models: ['test-model'],
  default_model: 'test-model', max_output_tokens: 8192, key_configured: true,
};
const fixture = compile(`<script>
  import { tick } from 'svelte';
  import ApiBilling from '/src/lib/ApiBilling.svelte';
  let profile = $state(${JSON.stringify(officialProfile)});
  window.setProfile = async next => { profile = next; await tick(); };
</script>
<main>{#key profile.id + ':' + profile.protocol + ':' + profile.base_url}<ApiBilling id={profile.id} name={profile.name} />{/key}</main>`, {
  filename: 'ApiBillingFixture.svelte', generate: 'client', css: 'injected',
}).js.code;

async function openFixture(t) {
  const server = await createServer({
    configFile: false,
    root: fileURLToPath(new URL('../../', import.meta.url)),
    server: { host: '127.0.0.1', port: 0 },
    optimizeDeps: { exclude: ['@tauri-apps/api/core', '@tauri-apps/plugin-opener'] },
    plugins: [{
      name: 'api-billing-ipc-mocks', enforce: 'pre',
      resolveId(id) {
        if (id === '@tauri-apps/api/core') return '\0billing-core';
        if (id === '@tauri-apps/plugin-opener') return '\0billing-opener';
      },
      load(id) {
        if (id === '\0billing-core') return 'export const invoke = (command,args) => window.invokeMock(command,args);';
        if (id === '\0billing-opener') return 'export const openUrl = async url => { window.openedUrls.push(url); };';
      },
    }, svelte({ configFile: false }), {
      name: 'api-billing-fixture',
      resolveId(id) { if (id === '/api-billing-fixture.js') return '\0api-billing-fixture'; },
      load(id) { if (id === '\0api-billing-fixture') return fixture; },
      configureServer(server) {
        server.middlewares.use(async (req, res, next) => {
          if (req.url !== '/__api-billing') return next();
          res.setHeader('Content-Type', 'text/html');
          res.end(await server.transformIndexHtml(req.url, `<!doctype html><html><head><style>
            :root { --text:#302e28; --dim:#77736b; --faint:#a29e95; --border:#e5e3de; --border2:#dedbd3; --bg:#fff; --rail:#f1f0ec; --accent:#302e28; --accent-h:#47443c; --accent-soft:#dedbd3; --err:#c54a43; }
            * { box-sizing:border-box; }
            body { margin:0; font-family:system-ui; background:#fff; }
            main { margin:16px; max-width:680px; }
          </style></head><body><div id="app"></div><script type="module">
            window.calls=[]; window.openedUrls=[]; window.responders={}; window.pending={};
            window.invokeMock=(command,args)=>{
              window.calls.push({command,args});
              const response=window.responders[command];
              if (!response) return Promise.reject('Unexpected mocked command: '+command);
              if (response.delay) return new Promise((resolve,reject)=>{ window.pending[command]={resolve,reject}; });
              if (response.error) return Promise.reject(response.error);
              return Promise.resolve(structuredClone(response.value));
            };
            import { mount } from 'svelte'; import Fixture from '/api-billing-fixture.js';
            mount(Fixture, { target:document.querySelector('#app') }); window.ready=true;
          </script></body></html>`));
        });
      },
    }],
  });
  let browser;
  try {
    await server.listen();
    browser = await chromium.launch({ headless:true, channel:process.env.PLAYWRIGHT_CHANNEL || 'chrome' });
    const page = await browser.newPage({ viewport:{ width:600, height:820 } });
    page.setDefaultTimeout(10_000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
    await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    t.after(async () => { await browser?.close(); await server.close(); });
    return { page, errors, origin };
  } catch (error) {
    await browser?.close(); await server.close(); throw error;
  }
}

async function respond(page, command, value) {
  await page.evaluate(({ command, value }) => { window.responders[command] = { value }; }, { command, value });
}
async function calls(page, command) {
  return page.evaluate(command => window.calls.filter(call => call.command === command), command);
}

const settings = (extra = {}) => ({
  provider: 'anthropic', credential_configured: false,
  billing_url: 'https://platform.claude.com/settings/billing', ...extra,
});
const snapshot = (extra = {}) => ({
  fetched_at: '2026-10-19T13:00:00Z', period_start: '2026-10-01T00:00:00Z',
  period_end: '2026-10-19T13:00:00Z', currency: 'USD', month_cost: 12.3456,
  today_cost: 1.2, month_input_tokens: 12000, month_output_tokens: 678,
  cost_error: null, usage_error: null, ...extra,
});

test('API billing keeps credentials separate, requires manual queries and renders honest responsive reports', async t => {
  const { page, errors, origin } = await openFixture(t);
  async function reset(metadata = settings()) {
    await page.goto(`${origin}/__api-billing`);
    await page.waitForFunction(() => window.ready).catch(error => {
      throw new Error(`${error.message}; page errors: ${errors.join('; ')}`);
    });
    await respond(page, 'get_api_billing_settings', metadata);
    return page.getByRole('button', { name: 'Claude 官方 用量与账单', exact: true });
  }
  async function open(metadata = settings()) {
    const toggle = await reset(metadata);
    assert.equal((await calls(page, 'get_api_billing_settings')).length, 0, 'Mounting does not access credentials or query a provider');
    await toggle.click();
    await page.getByText('剩余额度', { exact: true }).waitFor();
    return toggle;
  }
  async function metrics() { return page.locator('.metrics dd').allTextContents(); }
  async function query(value = snapshot()) {
    await respond(page, 'query_api_billing', value);
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await page.getByText(/更新于/).waitFor();
  }

  await t.test('missing credential shows no invented balance and save does not trigger a remote report query', async () => {
    await open();
    assert.equal((await calls(page, 'get_api_billing_settings')).length, 1);
    assert.equal((await calls(page, 'query_api_billing')).length, 0);
    assert.equal(await page.getByText('暂不支持查询', { exact: true }).count(), 1);
    assert.deepEqual(await metrics(), ['—', '—', '—', '—']);
    assert.equal(await page.getByRole('button', { name: '刷新', exact: true }).isDisabled(), true);
    assert.equal(await page.locator('input').count(), 0);
    await page.getByRole('button', { name: '配置', exact: true }).click();
    const key = page.getByLabel(/Admin API Key/);
    assert.equal(await key.getAttribute('type'), 'password');
    assert.equal(await key.getAttribute('autocomplete'), 'off');
    assert.match(await page.locator('.warning').textContent(), /权限较高.*系统凭据库.*不用于聊天/);
    await key.fill('  not-a-real-admin-key  ');
    await respond(page, 'save_api_billing_key', settings({ credential_configured: true }));
    await page.getByRole('button', { name: '保存', exact: true }).click();
    await page.getByText('查询密钥已保存', { exact: true }).waitFor();
    assert.deepEqual(await calls(page, 'save_api_billing_key'), [{ command: 'save_api_billing_key', args: { id: officialProfile.id, apiKey: 'not-a-real-admin-key' } }]);
    assert.equal((await calls(page, 'query_api_billing')).length, 0, 'Saving billing credentials must not silently send an account-wide query');
    assert.equal(await page.locator('input').count(), 0);
    assert.equal(await page.getByRole('button', { name: '刷新', exact: true }).isEnabled(), true);
    await query();
    assert.deepEqual(await calls(page, 'query_api_billing'), [{ command: 'query_api_billing', args: { id: officialProfile.id } }]);
    assert.deepEqual(await metrics(), ['$12.3456', '$1.20', '12,000', '678']);
    assert.match(await page.locator('.billing-detail').textContent(), /组织账户 · UTC/);
    assert.match(await page.locator('.billing-detail').textContent(), /费用不含 Priority Tier/);
    assert.match(await page.locator('.fetched').textContent(), /10-19 13:00 UTC/);
    assert.equal(await page.getByText('暂不支持查询', { exact: true }).count(), 1, 'Spend is never relabelled remaining balance');
  });

  await t.test('unknown values stay dashes while genuine zero reports remain visible as zero', async () => {
    await open(settings({ credential_configured: true }));
    await query(snapshot({ month_cost: null, today_cost: 0, month_input_tokens: 0, month_output_tokens: null, cost_error: '费用报表暂不可用' }));
    assert.deepEqual(await metrics(), ['—', '$0.00', '0', '—']);
    assert.match(await page.getByRole('alert').textContent(), /费用查询：费用报表暂不可用/);
    await query(snapshot({ month_cost: 0, today_cost: 0, month_input_tokens: 0, month_output_tokens: 0 }));
    assert.deepEqual(await metrics(), ['$0.00', '$0.00', '0', '0']);
    assert.equal(await page.getByRole('alert').count(), 0);
  });

  await t.test('380px and 600px layouts keep reports, password form and actions compact without overflow', async () => {
    await open(settings({ credential_configured: true }));
    await query(snapshot({ month_cost: 1234567.123456, month_input_tokens: 123456789012345 }));
    await page.getByRole('button', { name: '更换', exact: true }).click();
    for (const width of [380, 600]) {
      await page.setViewportSize({ width, height: 820 });
      const dimensions = await page.locator('.api-billing').evaluate(element => ({
        bodyOverflow: document.documentElement.scrollWidth > innerWidth,
        panelOverflow: element.scrollWidth > element.clientWidth,
        height: element.getBoundingClientRect().height,
        formHeight: element.querySelector('input').getBoundingClientRect().height,
        actionHeight: element.querySelector('.save').getBoundingClientRect().height,
        metricsOverflow: [...element.querySelectorAll('.metrics > div')].some(metric => metric.scrollWidth > metric.clientWidth),
      }));
      assert.equal(dimensions.bodyOverflow, false);
      assert.equal(dimensions.panelOverflow, false);
      assert.equal(dimensions.metricsOverflow, false);
      assert.ok(dimensions.height < 430, `Billing details should stay compact, found ${dimensions.height}px`);
      assert.equal(dimensions.formHeight, 28);
      assert.ok(dimensions.actionHeight <= 28);
      await page.locator('.api-billing').screenshot({ path: `/tmp/pilot-api-billing-${width}.png` });
    }
  });

  await t.test('unsupported compatible providers never offer a credential form or initiate a report query', async () => {
    await open(settings({ provider: 'unsupported', billing_url: 'https://third-party.example/billing', credential_configured: true }));
    assert.match(await page.locator('.billing-detail').textContent(), /兼容服务尚未适配账单查询/);
    assert.equal(await page.getByRole('button', { name: /刷新|配置|更换|查看账单/ }).count(), 0);
    assert.equal(await page.locator('input, .metrics').count(), 0);
    assert.equal((await calls(page, 'query_api_billing')).length, 0);
    assert.equal((await calls(page, 'save_api_billing_key')).length, 0);
  });

  await t.test('a failed refresh removes stale spend data instead of implying the previous result is current', async () => {
    await open(settings({ credential_configured: true }));
    await query();
    await page.evaluate(() => { window.responders.query_api_billing = { error: '查询失败：权限不足（403）' }; });
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await page.getByRole('alert').waitFor();
    assert.match(await page.getByRole('alert').textContent(), /权限不足（403）/);
    assert.deepEqual(await metrics(), ['—', '—', '—', '—']);
    assert.equal(await page.locator('.fetched').count(), 0);
    assert.equal(await page.getByRole('button', { name: '刷新', exact: true }).isEnabled(), true);
  });

  await t.test('collapse clears unsaved secrets and ignores any delayed query completion', async () => {
    const toggle = await open(settings({ credential_configured: true }));
    await page.getByRole('button', { name: '更换', exact: true }).click();
    await page.getByLabel(/Admin API Key/).fill('not-a-real-transient-secret');
    await toggle.click();
    assert.equal(await page.locator('input').count(), 0);
    await toggle.click();
    await page.getByRole('button', { name: '更换', exact: true }).click();
    assert.equal(await page.getByLabel(/Admin API Key/).inputValue(), '');
    assert.equal((await calls(page, 'save_api_billing_key')).length, 0);
    await page.getByRole('button', { name: '取消', exact: true }).click();
    await page.evaluate(() => { window.responders.query_api_billing = { delay: true }; });
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await page.waitForFunction(() => !!window.pending.query_api_billing);
    await toggle.click();
    await page.evaluate(result => window.pending.query_api_billing.resolve(result), snapshot());
    await toggle.click();
    await page.getByText('剩余额度', { exact: true }).waitFor();
    assert.deepEqual(await metrics(), ['—', '—', '—', '—']);
    assert.equal(await page.locator('.fetched').count(), 0);
  });

  await t.test('changing profile endpoints destroys the previous report and ignores its delayed response', async () => {
    await open(settings({ credential_configured: true }));
    await page.evaluate(() => { window.responders.query_api_billing = { delay: true }; });
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await page.waitForFunction(() => !!window.pending.query_api_billing);
    const proxy = { ...officialProfile, base_url: 'https://compatible.example/v1' };
    await page.evaluate(profile => window.setProfile(profile), proxy);
    assert.equal(await page.locator('.billing-detail').count(), 0);
    await respond(page, 'get_api_billing_settings', settings({ provider: 'unsupported', credential_configured: false, billing_url: null }));
    await page.getByRole('button', { name: 'Claude 官方 用量与账单', exact: true }).click();
    await page.evaluate(result => window.pending.query_api_billing.resolve(result), snapshot());
    await page.getByText(/兼容服务尚未适配/).waitFor();
    assert.equal(await page.locator('.metrics, .fetched').count(), 0);
    assert.doesNotMatch(await page.locator('.billing-detail').textContent(), /12\.3456/);
  });

  await t.test('removing a billing credential requires confirmation and does not touch model credentials', async () => {
    await open(settings({ credential_configured: true }));
    const remove = page.getByRole('button', { name: '移除 Claude 官方 的查询密钥', exact: true });
    await remove.click();
    const confirmation = page.getByRole('alertdialog');
    assert.equal(await confirmation.count(), 1);
    assert.match(await confirmation.textContent(), /模型 API Key 和已有对话不会改变/);
    assert.equal((await calls(page, 'delete_api_billing_key')).length, 0);
    await confirmation.getByRole('button', { name: '取消', exact: true }).click();
    assert.equal((await calls(page, 'delete_api_billing_key')).length, 0);
    await remove.click();
    await respond(page, 'delete_api_billing_key', settings());
    await confirmation.getByRole('button', { name: '移除密钥', exact: true }).click();
    await page.getByText('需单独配置组织级查询密钥', { exact: true }).waitFor();
    assert.deepEqual(await calls(page, 'delete_api_billing_key'), [{ command: 'delete_api_billing_key', args: { id: officialProfile.id } }]);
    assert.equal(await confirmation.count(), 0);
    assert.equal(await page.getByRole('button', { name: '刷新', exact: true }).isDisabled(), true);
    assert.equal((await calls(page, 'save_api_profile')).length, 0);
    assert.equal((await calls(page, 'delete_api_profile')).length, 0);
  });

  await t.test('billing links open only allowlisted official pages and reject spoofed destinations', async () => {
    for (const metadata of [
      settings(),
      settings({ provider: 'openai', billing_url: 'https://platform.openai.com/settings/organization/billing/overview' }),
    ]) {
      await open(metadata);
      await page.getByRole('button', { name: '查看账单', exact: true }).click();
      assert.deepEqual(await page.evaluate(() => window.openedUrls), [metadata.billing_url]);
      assert.equal((await calls(page, 'query_api_billing')).length, 0);
    }
    for (const billing_url of [
      'https://platform.claude.com.evil.example/settings/billing',
      'https://platform.claude.com/settings/billing?secret=not-a-real-key',
      'javascript:alert(1)',
    ]) {
      await open(settings({ billing_url }));
      assert.equal(await page.getByRole('button', { name: '查看账单', exact: true }).count(), 0);
      assert.deepEqual(await page.evaluate(() => window.openedUrls), []);
    }
  });
  assert.deepEqual(errors, []);
});
