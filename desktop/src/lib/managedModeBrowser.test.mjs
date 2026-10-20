import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { compile } from 'svelte/compiler';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createServer } from 'vite';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');

const fixture = compile(`<script>
import ManagedModePicker from '/src/lib/ManagedModePicker.svelte';
let mode = $state('growth'), pending = $state(null), disabled = $state(false);
window.modeRequests = [];
</script>
<div class="card">
  <div class="row"><strong>UBNEO</strong><ManagedModePicker {mode} label={mode === 'growth' ? '增长托管 · 数据驱动' : '计划托管'} {disabled} onchange={next => { window.modeRequests.push(next); pending = next; }} /><span>L1 自动发布</span></div>
  <p>本周发布 0 / 上限 3 · 待审草稿 0 篇</p>
</div>
<button id="outside">其他区域</button>
<button id="disable" onclick={() => disabled = !disabled}>切换禁用</button>
{#if pending}<div role="dialog" aria-label="确认模式切换"><button onclick={() => { mode = pending; pending = null; }}>确认</button><button onclick={() => pending = null}>取消</button></div>{/if}
`, { filename: 'ManagedModeFixture.svelte', generate: 'client', css: 'injected' }).js.code;

test('managed mode: controlled switching, cancellation, compact icons, keyboard, disabled and viewport boundaries', async () => {
  const server = await createServer({
    configFile: false,
    root: fileURLToPath(new URL('../../', import.meta.url)),
    server: { host: '127.0.0.1', port: 0 },
    plugins: [svelte({ configFile: false }), {
      name: 'managed-mode-fixture',
      resolveId(id) { if (id === '/managed-mode-fixture.js') return '\0managed-mode-fixture'; },
      load(id) { if (id === '\0managed-mode-fixture') return fixture; },
      configureServer(server) {
        server.middlewares.use(async (req, res, next) => {
          if (req.url !== '/__managed_mode') return next();
          res.setHeader('Content-Type', 'text/html');
          res.end(await server.transformIndexHtml(req.url, `<!doctype html><style>:root{--text:#302e28;--dim:#77736b;--faint:#a29d93;--border:#ecebe6}body{font-family:system-ui;margin:16px;background:#faf9f7}.card{max-width:800px;margin:32px auto;background:white;border:1px solid #ecebe6;border-radius:16px;padding:20px}.row{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.row>span{font-size:11px;color:#547c43}p{color:#77736b;font-size:12px}</style><div id="app"></div><script type="module">import{mount}from'svelte';import Fixture from'/managed-mode-fixture.js';mount(Fixture,{target:document.querySelector('#app')});window.ready=true;</script>`));
        });
      },
    }],
  });
  let browser;
  try {
    await server.listen();
    browser = await chromium.launch({ headless: true, channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome' });
    const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__managed_mode`);
    await page.waitForFunction(() => window.ready);
    const trigger = () => page.getByRole('button', { name: /^托管模式：/ });

    // The current value changes only after the host accepts, never on selection.
    await trigger().click();
    assert.equal(await page.getByRole('menuitemradio').count(), 2);
    assert.equal(await page.getByRole('menuitemradio', { name: '增长托管' }).getAttribute('aria-checked'), 'true');
    assert.deepEqual(await page.getByRole('menuitemradio').evaluateAll(items => items.map(el => {
      const icon = el.querySelector('.mode-icon').getBoundingClientRect(), label = el.querySelector('span').getBoundingClientRect();
      return { width: icon.width, height: icon.height, centered: Math.abs(icon.y + icon.height / 2 - label.y - label.height / 2) < 1 };
    })), [{ width: 12, height: 12, centered: true }, { width: 12, height: 12, centered: true }]);
    assert.equal(await trigger().evaluate(el => el.getBoundingClientRect().height), 20);
    await page.screenshot({ path: '/tmp/pilot-managed-mode-menu.png' });
    await page.getByRole('menuitemradio', { name: '计划托管' }).click();
    assert.match(await trigger().textContent(), /增长托管 · 数据驱动/);
    await page.getByRole('button', { name: '取消', exact: true }).click();
    assert.match(await trigger().textContent(), /增长托管 · 数据驱动/);
    await trigger().click();
    await page.getByRole('menuitemradio', { name: '计划托管' }).click();
    await page.getByRole('button', { name: '确认', exact: true }).click();
    assert.match(await trigger().textContent(), /计划托管/);
    await trigger().click();
    await page.getByRole('menuitemradio', { name: '增长托管' }).click();
    await page.getByRole('button', { name: '确认', exact: true }).click();
    assert.match(await trigger().textContent(), /增长托管 · 数据驱动/);
    assert.deepEqual(await page.evaluate(() => window.modeRequests), ['plan', 'plan', 'growth']);

    // Choosing the current option does not create a change/confirmation.
    await trigger().click();
    await page.getByRole('menuitemradio', { name: '增长托管' }).click();
    assert.equal(await page.getByRole('dialog').count(), 0);
    assert.equal(await page.evaluate(() => window.modeRequests.length), 3);

    // Arrow keys select focus; Enter requests a change; Escape returns focus.
    await trigger().focus();
    await page.keyboard.press('ArrowDown');
    await page.waitForFunction(() => document.activeElement?.getAttribute('aria-checked') === 'true');
    await page.keyboard.press('Home');
    assert.equal(await page.getByRole('menuitemradio', { name: '计划托管' }).evaluate(el => el === document.activeElement), true);
    await page.keyboard.press('End');
    assert.equal(await page.getByRole('menuitemradio', { name: '增长托管' }).evaluate(el => el === document.activeElement), true);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    assert.equal(await page.getByRole('dialog').count(), 1);
    await page.getByRole('button', { name: '取消', exact: true }).click();
    await trigger().click();
    await page.keyboard.press('Escape');
    assert.equal(await page.getByRole('menu').count(), 0);
    assert.equal(await trigger().evaluate(el => el === document.activeElement), true);
    await trigger().click();
    await page.locator('#outside').click();
    assert.equal(await page.getByRole('menu').count(), 0);
    await page.locator('#disable').click();
    assert.equal(await trigger().isDisabled(), true);
    await page.locator('#disable').click();

    // Fixed menu remains visible at narrow/bottom/right edges, without layout overflow.
    await page.setViewportSize({ width: 360, height: 320 });
    await page.addStyleTag({ content: '.card{position:fixed;right:8px;bottom:8px;padding:10px;max-width:300px;margin:0}.row{justify-content:flex-end}' });
    await trigger().click();
    const bounds = await page.getByRole('menu').boundingBox();
    assert.ok(bounds && bounds.x >= 8 && bounds.y >= 8 && bounds.x + bounds.width <= 352 && bounds.y + bounds.height <= 312);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.screenshot({ path: '/tmp/pilot-managed-mode-narrow.png' });
    assert.deepEqual(errors, []);
  } finally {
    await browser?.close();
    await server.close();
  }
});
