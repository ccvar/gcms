import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { compile } from 'svelte/compiler';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createServer } from 'vite';

// Synthetic model choices only. No app profile, IPC, credentials or API calls.
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const options = [
  ...['claude-opus-5-5', 'claude-fable-5-1', 'claude-sonnet-5-5'].map(model => ({ value: `api:claude::${model}`, label: model, sub: 'API · Claude 官方', icon: 'claude', apiProtocol: 'anthropic_messages' })),
  { value: 'api:openai::gpt-6-sol', label: 'GPT-6 Sol API', sub: 'Responses API', icon: 'openai', apiProtocol: 'openai_responses' },
  { value: 'api:chat::gpt-6-sol', label: 'GPT-6 Sol 兼容接口', sub: 'Chat Completions API', icon: 'openai', apiProtocol: 'openai_chat' },
  { value: 'api:chat::custom-model', label: '自定义 API 模型', apiProtocol: 'openai_chat' },
  { value: 'claude::claude-opus-5-5', label: 'Opus 5.5 CLI', icon: 'claude' },
  { value: 'codex::gpt-6-sol', label: 'GPT-6 Sol CLI', icon: 'codex' },
  { value: 'codex::gpt-6-luna', label: 'GPT-6 Luna CLI', icon: 'codex' },
];
const fixture = compile(`<script>
  import { tick } from 'svelte';
  import ModelFx from '/src/lib/ModelFx.svelte';
  import { effortAfterModelPick } from '/src/lib/modelSelection';
  const options = ${JSON.stringify(options)};
  let value = $state(options[0].value), effort = $state(''), lockModel = $state(false);
  window.efforts = []; window.picks = []; window.acceptPick = true;
  window.setModelFixture = async next => {
    value = next.value ?? options[0].value; effort = next.effort ?? ''; lockModel = next.lockModel ?? false;
    window.efforts = []; window.picks = []; window.acceptPick = true; await tick();
  };
  function pick(next) {
    window.picks.push(next);
    if (!window.acceptPick) return;
    effort = effortAfterModelPick(value, next, effort, options); value = next;
  }
</script>
<main><div class="composer"><p>任意聊点什么…</p><div class="controls"><ModelFx {options} {value} {effort} {lockModel} onpick={pick} oneffort={v=>{effort=v;window.efforts.push(v);}} /></div></div><output id="selection">{value}|{effort}</output></main>`, {
  filename: 'ApiReasoningFixture.svelte', generate: 'client', css: 'injected',
}).js.code;

test('API reasoning controls follow native capabilities and persist intentional selections', async t => {
  const server = await createServer({
    configFile: false, root: fileURLToPath(new URL('../../', import.meta.url)),
    server: { host: '127.0.0.1', port: 0 },
    plugins: [svelte({ configFile: false }), {
      name: 'api-reasoning-fixture',
      resolveId(id) { if (id === '/api-reasoning-fixture.js') return '\0api-reasoning-fixture'; },
      load(id) { if (id === '\0api-reasoning-fixture') return fixture; },
      configureServer(server) {
        server.middlewares.use(async (req, res, next) => {
          if (req.url !== '/__api-reasoning') return next();
          res.setHeader('Content-Type', 'text/html');
          res.end(await server.transformIndexHtml(req.url, `<!doctype html><html><head><style>
            :root{--text:#302e28;--dim:#77736b;--faint:#a29e95;--border:#e5e3de;--border2:#dedbd3;--bg:#fff;--err:#c54a43}
            *{box-sizing:border-box}body{margin:0;font-family:system-ui;background:#faf9f7}main{position:absolute;inset:0;padding:16px;display:flex;align-items:flex-end}.composer{width:100%;border:1px solid #dedbd3;border-radius:14px;padding:12px;background:#fff}.composer p{font-size:13px;color:#aaa;margin:0 0 24px}.controls{display:flex;justify-content:flex-end}output{display:none}
          </style></head><body><div id="app"></div><script type="module">
            import { mount } from 'svelte'; import Fixture from '/api-reasoning-fixture.js';
            mount(Fixture, { target:document.querySelector('#app') }); window.ready=true;
          </script></body></html>`));
        });
      },
    }],
  });
  let browser;
  try {
    await server.listen();
    browser = await chromium.launch({ headless: true, channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome' });
    const page = await browser.newPage({ viewport: { width: 420, height: 700 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.setDefaultTimeout(10_000);
    const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
    await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    await page.goto(`${origin}/__api-reasoning`);
    await page.waitForFunction(() => window.ready);
    async function setup(config = {}) {
      await page.keyboard.press('Escape');
      await page.evaluate(config => window.setModelFixture(config), config);
      await page.locator('.fx-trigger').click();
      await page.locator('.fx-menu').waitFor();
    }
    const labels = () => page.locator('.fx-stop-labels button').allTextContents();
    const effortCalls = () => page.evaluate(() => window.efforts);

    await t.test('all screenshot Claude models expose default plus all five levels', async () => {
      for (const model of ['claude-opus-5-5', 'claude-fable-5-1', 'claude-sonnet-5-5']) {
        await setup({ value: `api:claude::${model}` });
        assert.deepEqual(await labels(), ['默认', '低', '中', '高', '极高', '最大']);
        assert.match(await page.locator('.fx-trigger').textContent(), /API · 默认/);
        assert.deepEqual(await effortCalls(), [], 'Rendering must not mutate saved choices');
        await page.getByRole('button', { name: '最大', exact: true }).click();
        assert.deepEqual(await effortCalls(), ['api:max']);
        assert.match(await page.locator('.fx-trigger').textContent(), /API · 最大/);
        await page.getByRole('button', { name: '默认', exact: true }).click();
        assert.deepEqual(await effortCalls(), ['api:max', '']);
      }
    });

    await t.test('Responses and compatible Chat expose different, honest tool-compatible levels', async () => {
      await setup({ value: 'api:openai::gpt-6-sol', effort: 'api:high' });
      assert.deepEqual(await labels(), ['默认', '无', '低', '中', '高', '极高', '最大']);
      assert.equal(await page.getByRole('button', { name: '高', exact: true }).getAttribute('aria-pressed'), 'true');
      await setup({ value: 'api:chat::gpt-6-sol' });
      assert.deepEqual(await labels(), ['默认', '无']);
      assert.match(await page.locator('.fx-menu').textContent(), /请使用 OpenAI Responses 接口/);
      await page.getByRole('button', { name: '无', exact: true }).click();
      assert.deepEqual(await effortCalls(), ['api:none']);
    });

    await t.test('unknown models have no fake levels; invalid explicit effort can be cleared', async () => {
      await setup({ value: 'api:chat::custom-model' });
      assert.equal(await page.locator('.fx-slider').count(), 0);
      assert.match(await page.locator('.fx-menu').textContent(), /暂未确认可用推理档位/);
      await setup({ value: 'api:chat::custom-model', effort: 'api:high' });
      assert.match(await page.locator('.fx-trigger').textContent(), /待调整/);
      assert.equal(await page.getByRole('status').count(), 1);
      assert.deepEqual(await effortCalls(), []);
      await page.getByRole('button', { name: '恢复默认', exact: true }).click();
      assert.deepEqual(await effortCalls(), ['']);
      assert.match(await page.locator('.fx-trigger').textContent(), /API · 默认/);
    });

    await t.test('legacy ignored raw efforts remain provider-default instead of activating costlier reasoning', async () => {
      for (const effort of ['high', 'max', 'ultra']) {
        await setup({ effort });
        assert.match(await page.locator('.fx-trigger').textContent(), /API · 默认/);
        assert.equal(await page.getByRole('status').count(), 0);
        assert.equal(await page.getByRole('button', { name: '默认', exact: true }).getAttribute('aria-pressed'), 'true');
        assert.deepEqual(await effortCalls(), []);
      }
    });

    await t.test('model switches preserve only valid same-protocol effort and cannot bypass cancellation', async () => {
      await setup({ effort: 'api:max' });
      await page.locator('.fx-opt').filter({ hasText: 'claude-sonnet-5-5' }).click();
      assert.match(await page.locator('#selection').textContent(), /claude-sonnet-5-5\|api:max$/);
      await page.locator('.fx-opt').filter({ hasText: 'GPT-6 Sol API' }).click();
      assert.equal(await page.locator('#selection').textContent(), 'api:openai::gpt-6-sol|');
      await page.getByRole('button', { name: '高', exact: true }).click();
      await page.locator('.fx-opt').filter({ hasText: 'GPT-6 Sol CLI' }).click();
      assert.equal(await page.locator('#selection').textContent(), 'codex::gpt-6-sol|');
      await page.getByRole('button', { name: '极致', exact: true }).click();
      await page.locator('.fx-opt').filter({ hasText: 'claude-opus-5-5' }).click();
      assert.equal(await page.locator('#selection').textContent(), 'api:claude::claude-opus-5-5|');
      await page.getByRole('button', { name: '高', exact: true }).click();
      await page.evaluate(() => { window.acceptPick = false; });
      await page.locator('.fx-opt').filter({ hasText: 'GPT-6 Sol CLI' }).click();
      assert.equal(await page.locator('#selection').textContent(), 'api:claude::claude-opus-5-5|api:high');
    });

    await t.test('running turn locks models but effort stays editable for the next turn', async () => {
      await setup({ lockModel: true });
      assert.equal(await page.locator('.fx-opt:enabled').count(), 0);
      await page.getByRole('button', { name: '高', exact: true }).click();
      assert.deepEqual(await effortCalls(), ['api:high']);
    });

    await t.test('CLI efforts, Luna cap and Fast mode remain unchanged', async () => {
      await setup({ value: 'codex::gpt-6-sol', effort: 'ultra' });
      assert.deepEqual(await labels(), ['自动', '低', '中', '高', '极高', '最高', '极致']);
      await setup({ value: 'codex::gpt-6-luna', effort: 'ultra' });
      assert.deepEqual(await labels(), ['自动', '低', '中', '高', '极高', '最高']);
      assert.match(await page.locator('.fx-trigger').textContent(), /最高/);
      await setup({ value: 'claude::claude-opus-5-5', effort: 'high' });
      assert.equal(await page.locator('.fx-fast').count(), 1);
      await page.getByRole('button', { name: '最高', exact: true }).click();
      assert.deepEqual(await effortCalls(), ['max']);
    });

    await t.test('narrow composer keeps dropdown and labels visible without overflow', async () => {
      await setup({ value: 'api:claude::claude-sonnet-5-5' });
      await page.screenshot({ path: '/tmp/pilot-api-reasoning-ui.png' });
      await page.getByRole('button', { name: '最大', exact: true }).click();
      await page.waitForFunction(() => {
        const track = document.querySelector('.fx-slider'), thumb = document.querySelector('.fx-thumb');
        return track?.classList.contains('max') && thumb.getBoundingClientRect().left > track.getBoundingClientRect().left + track.clientWidth * .8;
      });
      await page.screenshot({ path: '/tmp/pilot-api-reasoning-max.png' });
      const geometry = await page.locator('.fx-menu').evaluate(el => {
        const rect = el.getBoundingClientRect();
        return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom,
          viewport: window.innerWidth, height: window.innerHeight, scrollWidth: document.documentElement.scrollWidth };
      });
      assert.ok(geometry.left >= 0 && geometry.right <= geometry.viewport);
      assert.ok(geometry.top >= 0 && geometry.bottom <= geometry.height);
      assert.equal(geometry.scrollWidth, geometry.viewport);
      await setup({ effort: 'api:ultra' });
      await page.screenshot({ path: '/tmp/pilot-api-reasoning-invalid.png' });
    });
    assert.deepEqual(errors, []);
  } finally {
    await browser?.close(); await server.close();
  }
});
