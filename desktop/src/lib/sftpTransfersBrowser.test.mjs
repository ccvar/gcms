import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { compile } from 'svelte/compiler';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createServer } from 'vite';

// This fixture imports only the transfer popover and its pure history helpers.
// All records and action callbacks are synthetic; external requests are blocked.
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fixture = compile(`<script>
  import { tick } from 'svelte';
  import SftpTransfers from '/src/lib/SftpTransfers.svelte';
  import { createTransfer, finishTransfer, isTransferActive } from '/src/lib/sftpTransfers.ts';
  const base = (id, extra = {}) => ({ ...createTransfer({
    id, connId: 'test-server', connectionName: 'Production-server-with-a-very-long-name',
    name: id + '-production-backup-all-sites-and-databases-with-a-very-long-filename.tar.gz',
    direction: 'download', remotePath: '/srv/very-long-directory/backup.tar.gz',
  }, 1790000000000), ...extra });
  const initial = [
    base('complete-download', { status:'completed', localPath:'/local/very-long-download-directory/backup.tar.gz', finishedAt:1790000001000 }),
    base('running', { status:'running', phase:'downloading', bytes:6.1*1024*1024, totalBytes:86*1024*1024, files:1, totalFiles:4, updatedAt:1790000010000 }),
    base('queued', { status:'queued', direction:'upload' }),
    base('failed', { status:'failed', error:'Permission denied: a very long message explaining the remote transfer failure' }),
    base('cancelled', { status:'cancelled' }),
    base('interrupted', { status:'interrupted', error:'应用重新启动，传输已中断' }),
    base('complete-upload', { status:'completed', direction:'upload' }),
  ];
  let records = $state(initial);
  let options = $state({ busy:false, canDownload:true, selectedName:'selected-config.json', cancellableIds:['running'] });
  window.actions = [];
  window.setTransfers = async (next) => { records = next; await tick(); };
  window.setOptions = async (next) => { Object.assign(options, next); await tick(); };
  window.currentTransfers = () => records;
  const cancel = id => { window.actions.push({ type:'cancel', id }); records = finishTransfer(records, id, 'cancelled'); };
  const clear = () => { window.actions.push({ type:'clear' }); records = records.filter(isTransferActive); };
</script>
<div class="files-wrap"><div id="toolbar"><SftpTransfers {records} {...options}
  onupload={() => window.actions.push({ type:'upload' })}
  ondownload={() => window.actions.push({ type:'download' })}
  oncancel={cancel} onclear={clear}
  onreveal={record => window.actions.push({ type:'reveal', id:record.id, path:record.localPath })} /></div></div>
<button id="outside">Outside the transfer popover</button>`, {
  filename: 'SftpTransfersFixture.svelte', generate: 'client', css: 'injected',
}).js.code;

test('transfer popover renders compact history and routes user actions without backend access', async t => {
  const server = await createServer({
    configFile: false,
    root: fileURLToPath(new URL('../../', import.meta.url)),
    server: { host: '127.0.0.1', port: 0 },
    plugins: [svelte({ configFile: false }), {
      name: 'sftp-transfers-fixture',
      resolveId(id) { if (id === '/sftp-transfers-fixture.js') return '\0sftp-transfers-fixture'; },
      load(id) { if (id === '\0sftp-transfers-fixture') return fixture; },
      configureServer(server) {
        server.middlewares.use(async (req, res, next) => {
          if (req.url !== '/__sftp-transfers') return next();
          res.setHeader('Content-Type', 'text/html');
          res.end(await server.transformIndexHtml(req.url, `<!doctype html><html><head><style>
            :root { --text:#302e28; --dim:#77736b; --border:#e5e3de; --bg:#fff; --rail:#f1f0ec; --accent:#302e28; --err:#c54a43; }
            body { margin:0; font-family:system-ui; background:#faf9f7; }
            .files-wrap { container-type:inline-size; transform:translateY(12px); width:calc(100% - 40px); margin:20px; overflow:hidden; }
            #toolbar { height:40px; display:flex; align-items:center; justify-content:flex-end; padding:0 16px; }
            #outside { position:fixed; bottom:20px; left:12px; }
          </style></head><body><div id="app"></div><script type="module">
            import { mount } from 'svelte'; import Fixture from '/sftp-transfers-fixture.js';
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
    const page = await browser.newPage({ viewport:{ width:380, height:680 } });
    page.setDefaultTimeout(10_000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
    await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    await page.goto(`${origin}/__sftp-transfers`);
    await page.waitForFunction(() => window.ready).catch(error => {
      throw new Error(`${error.message}; page errors: ${errors.join('; ')}`);
    });
    const trigger = page.getByRole('button', { name:'传输', exact:true });
    const dialog = page.getByRole('dialog', { name:'传输记录' });
    const setOptions = options => page.evaluate(options => window.setOptions(options), options);

    await t.test('one 24px trigger opens history with active records first and all terminal status labels', async () => {
      const bounds = await trigger.boundingBox();
      assert.equal(bounds.width, 24);
      assert.equal(bounds.height, 24);
      assert.equal(await trigger.getAttribute('title'), '传输');
      assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
      assert.equal(await dialog.count(), 0);
      await trigger.click();
      assert.equal(await trigger.getAttribute('aria-expanded'), 'true');
      assert.equal(await dialog.evaluate(element => element === document.activeElement), true);
      assert.equal(await dialog.evaluate(element => element.parentElement === document.body), true, 'Popover must escape transformed size containers');
      assert.deepEqual(await dialog.locator('.status').allTextContents(), ['下载中', '等待中', '已完成', '失败', '已取消', '已中断', '已完成']);
      assert.equal(await dialog.getByRole('img', { name:'上传', exact:true }).count(), 2);
      assert.equal(await dialog.getByRole('img', { name:'下载', exact:true }).count(), 5);
      assert.equal(await dialog.locator('progress').nth(0).evaluate(element => element.value), 7);
      assert.equal(await dialog.locator('progress').nth(1).evaluate(element => element.matches(':indeterminate')), true);
      assert.equal(await dialog.locator('.rate').count(), 1);
      assert.match(await dialog.locator('.rate').textContent(), /平均 .*\/s · 剩余约/);
    });

    await t.test('280px and 380px viewports keep long names, controls and compact rows inside the popover', async () => {
      for (const width of [280, 380]) {
        await page.setViewportSize({ width, height:680 });
        await page.waitForFunction(width => document.querySelector('.transfer-panel').getBoundingClientRect().width <= width - 16, width);
        const metrics = await dialog.evaluate(element => {
          const bounds = element.getBoundingClientRect();
          const rows = [...element.querySelectorAll('.transfer-row')];
          return {
            width:bounds.width, left:bounds.left, right:bounds.right, bottom:bounds.bottom,
            overflow:element.scrollWidth > element.clientWidth,
            pageOverflow:document.documentElement.scrollWidth > innerWidth,
            rows:rows.map(row => ({ height:row.getBoundingClientRect().height, overflow:row.scrollWidth > row.clientWidth,
              truncated:row.querySelector('.title b').scrollWidth > row.querySelector('.title b').clientWidth })),
            actionSizes:[...element.querySelectorAll('.icon-button')].map(button => { const b=button.getBoundingClientRect(); return [b.width,b.height]; }),
          };
        });
        assert.ok(metrics.width <= width - 16);
        assert.ok(metrics.left >= 8 && metrics.right <= width - 8);
        assert.ok(metrics.bottom <= 680);
        assert.equal(metrics.overflow, false);
        assert.equal(metrics.pageOverflow, false);
        for (const row of metrics.rows) {
          assert.equal(row.overflow, false);
          assert.equal(row.truncated, true);
          assert.ok(row.height <= 100, `Expected compact history row, got ${row.height}px`);
        }
        for (const size of metrics.actionSizes) assert.deepEqual(size, [24, 24]);
        await dialog.screenshot({ path:`/tmp/pilot-sftp-transfers-${width}.png` });
      }
    });

    await t.test('Escape returns focus to the trigger and clicking outside dismisses the panel', async () => {
      await page.keyboard.press('Escape');
      assert.equal(await dialog.count(), 0);
      assert.equal(await trigger.evaluate(element => element === document.activeElement), true);
      await trigger.press('Enter');
      assert.equal(await dialog.count(), 1);
      await page.locator('#outside').click();
      assert.equal(await dialog.count(), 0);
      await trigger.click();
    });

    await t.test('upload, selected download and reveal actions send the intended callback data', async () => {
      const upload = dialog.getByRole('button', { name:'上传文件…', exact:true });
      const download = dialog.getByRole('button', { name:'下载所选…', exact:true });
      await upload.click();
      await download.click();
      await dialog.getByRole('button', { name:/^在文件夹中显示 complete-download-/ }).click();
      assert.deepEqual(await page.evaluate(() => window.actions), [
        { type:'upload' }, { type:'download' },
        { type:'reveal', id:'complete-download', path:'/local/very-long-download-directory/backup.tar.gz' },
      ]);
      await setOptions({ busy:true });
      assert.equal(await upload.isDisabled(), true);
      assert.equal(await download.isDisabled(), true);
      await setOptions({ busy:false, canDownload:false });
      assert.equal(await upload.isEnabled(), true);
      assert.equal(await download.isDisabled(), true);
      await setOptions({ canDownload:true });
    });

    await t.test('only cancellable active rows cancel, and clearing settled history keeps active work', async () => {
      const queued = dialog.getByRole('button', { name:/^取消传输 queued-/ });
      assert.equal(await queued.isDisabled(), true);
      const running = dialog.getByRole('button', { name:/^取消传输 running-/ });
      assert.equal(await running.isEnabled(), true);
      await running.click();
      assert.deepEqual(await page.evaluate(() => window.actions.at(-1)), { type:'cancel', id:'running' });
      await dialog.getByRole('button', { name:'清除记录', exact:true }).click();
      assert.deepEqual(await page.evaluate(() => window.currentTransfers().map(record => record.id)), ['queued']);
      assert.equal(await dialog.locator('.transfer-row').count(), 1);
      assert.equal(await dialog.getByRole('button', { name:'清除记录', exact:true }).isDisabled(), true);
      assert.equal(await dialog.locator('.summary').textContent(), '1 项进行中');
      await page.evaluate(() => window.setTransfers([]));
      assert.equal(await dialog.getByText('暂无传输记录', { exact:true }).count(), 1);
      assert.equal(await trigger.locator('.activity').count(), 0);
    });
    assert.deepEqual(errors, []);
  } finally {
    await browser?.close();
    await server.close();
  }
});
