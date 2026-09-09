import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { createServer } from 'vite';

// Browser regression for the real global handler + CodeMirror. IPC is mocked:
// no OS clipboard access, native menu clicks or remote file writes occur.
// PLAYWRIGHT_MODULE can point to an externally provisioned Playwright runtime.
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const source = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
const start = source.indexOf('  function onCtxMenu(');
const end = source.indexOf('  // ---------- 助手消息 Markdown', start);
assert.ok(start >= 0 && end > start);
const handler = ts.transpileModule(source.slice(start, end), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
const inputs = ['text', 'search', 'url', 'tel', 'email', 'password', 'number', 'checkbox', 'range', 'file', 'color', 'date', 'button'];
const html = `<!doctype html><html><body>
${inputs.map(type => `<input id="${type}" type="${type}" aria-label="${type}">`).join('')}
<input id="default"><input id="readonly" readonly value="read only">
<input id="disabled" disabled><input id="aria-disabled" aria-disabled="true"><fieldset disabled><input id="fieldset"></fieldset>
<textarea id="textarea">abcdef</textarea><textarea id="readonly-area" readonly>copy only</textarea>
<div id="rich" contenteditable="true"><span id="rich-child">rich text</span><span id="island" contenteditable="false">locked</span></div>
<div id="plain" contenteditable="plaintext-only"><span id="plain-child">plain text</span></div>
<div contenteditable="true" aria-readonly="true"><span id="aria-readonly">read only</span></div>
<div inert><textarea id="inert"></textarea></div>
<p id="message">ordinary message</p><div id="blank">blank</div>
<div id="files">file menu</div><div class="xterm"><textarea id="terminal"></textarea></div>
<div id="editor" style="height:240px;border:1px solid gray"></div>
<script type="module">
import { editMenuTarget } from '/src/lib/editMenu.ts';
import { createCodeEditor } from '/src/lib/codeEditorRuntime.ts';
window.menuCalls = []; window.terminalSelected = false; window.terminalCopies = 0;
const invoke = async (command, args) => { window.menuCalls.push({ command, ...args }); };
const term = { hasSelection: () => window.terminalSelected };
const copyTermSelection = async () => { window.terminalCopies++; };
${handler}
window.addEventListener('contextmenu', onCtxMenu);
document.querySelector('#files').addEventListener('contextmenu', e => e.preventDefault());
window.editor = createCodeEditor(document.querySelector('#editor'), {
  value: '{"example":"value"}', onchange: () => {}, onstatus: () => {}, onsave: () => {},
});
await window.editor.language('json');
window.editor.configure(false, false, '  ');
window.__ready = true;
</script></body></html>`;

test('native edit menu routing preserves editable hosts, selection and specialized menus', async () => {
  const server = await createServer({
    configFile: false, root: fileURLToPath(new URL('../../', import.meta.url)),
    server: { host: '127.0.0.1', port: 0 },
    plugins: [{ name: 'edit-menu-fixture', configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.url !== '/__edit-menu-test') return next();
        res.setHeader('Content-Type', 'text/html'); res.end(html);
      });
    } }],
  });
  let browser;
  try {
    await server.listen();
    browser = await chromium.launch({ headless: true, channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome' });
    const page = await browser.newPage();
    page.setDefaultTimeout(15_000);
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    const port = server.httpServer.address().port;
    await page.goto(`http://127.0.0.1:${port}/__edit-menu-test`);
    await page.waitForFunction(() => window.__ready);
    const menu = async selector => page.evaluate(selector => {
      window.menuCalls = [];
      document.querySelector(selector).dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }));
      return window.menuCalls;
    }, selector);
    for (const type of ['default', 'text', 'search', 'url', 'tel', 'email', 'password', 'number', 'textarea', 'rich-child', 'plain-child']) {
      assert.deepEqual(await menu(`#${type}`), [{ command: 'show_edit_menu', editable: true }], type);
    }
    for (const id of ['readonly', 'readonly-area', 'aria-readonly']) {
      assert.deepEqual(await menu(`#${id}`), [{ command: 'show_edit_menu', editable: false }], id);
    }
    await page.evaluate(() => window.getSelection().removeAllRanges());
    for (const id of ['disabled', 'aria-disabled', 'fieldset', 'inert', 'checkbox', 'range', 'file', 'color', 'date', 'button', 'island', 'blank', 'files']) {
      assert.deepEqual(await menu(`#${id}`), [], id);
    }
    await page.evaluate(() => {
      const range = document.createRange(); range.selectNodeContents(document.querySelector('#message'));
      const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range);
    });
    assert.deepEqual(await menu('#message'), [{ command: 'show_edit_menu', editable: false }]);
    await page.evaluate(() => window.getSelection().removeAllRanges());

    // A selected textarea remains the first responder with the same selection.
    await page.evaluate(() => { const t = document.querySelector('#textarea'); t.focus(); t.setSelectionRange(1, 4); });
    await menu('#textarea');
    assert.deepEqual(await page.locator('#textarea').evaluate(el => [el.selectionStart, el.selectionEnd, el === document.activeElement]), [1, 4, true]);
    await page.keyboard.insertText('replacement');
    assert.equal(await page.locator('#textarea').inputValue(), 'areplacementef');

    // Syntax-highlight spans resolve to .cm-content; insertion and undo remain
    // CodeMirror transactions rather than direct DOM text replacement.
    await page.evaluate(() => { window.editor.focus(); window.editor.view.dispatch({ selection: { anchor: 12, head: 17 } }); });
    const before = await page.evaluate(() => window.editor.view.state.doc.toString());
    assert.deepEqual(await menu('.cm-line span'), [{ command: 'show_edit_menu', editable: true }]);
    assert.deepEqual(await page.evaluate(() => {
      const s = window.editor.view.state.selection.main;
      return [s.from, s.to, document.activeElement === window.editor.view.contentDOM];
    }), [12, 17, true]);
    await page.keyboard.insertText('new text');
    await page.waitForFunction(before => window.editor.view.state.doc.toString() !== before, before);
    await page.evaluate(() => window.editor.undo());
    assert.equal(await page.evaluate(() => window.editor.view.state.doc.toString()), before);
    await page.evaluate(() => { window.editor.configure(true, false, '  '); window.getSelection().removeAllRanges(); });
    assert.deepEqual(await menu('.cm-line span'), []);

    assert.deepEqual(await menu('#terminal'), [{ command: 'show_edit_menu', editable: true }]);
    assert.equal(await page.locator('#terminal').evaluate(el => el === document.activeElement), true);
    await page.evaluate(() => { window.terminalSelected = true; });
    assert.deepEqual(await menu('#terminal'), []);
    assert.equal(await page.evaluate(() => window.terminalCopies), 1);
    assert.deepEqual(errors, []);
  } finally {
    await browser?.close();
    await server.close();
  }
});
