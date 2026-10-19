import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

const page = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
function section(start, end) {
  const a = page.indexOf(start), b = page.indexOf(end, a);
  assert.ok(a >= 0 && b > a);
  return ts.transpileModule(page.slice(a, b), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
}
const initial = section('  let lPerm =', '  let lEffort =');
const select = section('  function selectLauncherTask(', '  async function toggleSiteStatus(');

test('restoring the free-chat launcher defaults to full for local and API models', () => {
  for (const brain of ['claude', 'codex', 'grok', 'api:test']) {
    for (const perm of [undefined, 'plan', 'ask', 'auto', 'full']) {
      const ctx = vm.createContext({ prefs: { taskType: 'workspace', brain, perm }, $state: v => v });
      vm.runInContext(initial, ctx);
      assert.equal(vm.runInContext('lPerm', ctx), 'full');
    }
  }
});

test('switching to free chat clears site binding and defaults to full without changing existing conversations', () => {
  for (const perm of ['plan', 'ask', 'auto', 'full']) {
    const existing = { perm_mode: perm };
    const ctx = vm.createContext({
      lTask: 'sitebuild', lPerm: perm, lSite: 'example', lSites: ['example'],
      activeConn: { kind: 'gcms' }, sites: [{ slug: 'example' }],
      activeConv: existing, threadPerm: perm,
    });
    vm.runInContext(select, ctx);
    ctx.selectLauncherTask('workspace');
    assert.equal(ctx.lPerm, 'full');
    assert.equal(ctx.lTask, 'workspace');
    assert.equal(ctx.lSite, '');
    assert.equal(ctx.lSites.length, 0);
    assert.equal(existing.perm_mode, perm);
    assert.equal(ctx.threadPerm, perm);
  }
});

test('new free chats reset the launcher default, without changing stored chats or other workspaces', () => {
  const code = section('  function newChat()', '  async function openConv(');
  for (const isWorkspaceChat of [true, false]) {
    const existing = { perm_mode: 'ask' };
    const ctx = vm.createContext({
      openConvSeq: 0, activeConvId: 'existing', activeConv: existing,
      lDraft: '', draft: '', attachments: [], lPerm: 'auto',
      isWorkspaceChat, isSshConn: false, sites: [], lSite: '', lBrain: 'api:test',
      viewAfterConvGone: () => {}, brainUsable: () => true,
    });
    vm.runInContext(code, ctx);
    ctx.newChat();
    assert.equal(ctx.lPerm, isWorkspaceChat ? 'full' : 'auto');
    assert.equal(existing.perm_mode, 'ask');
    assert.equal(ctx.activeConv, null);
  }
});

test('site operations and site-building defaults stay unchanged', () => {
  for (const taskType of ['siteops', 'sitebuild']) {
    for (const perm of ['plan', 'ask', 'auto', 'full']) {
      const ctx = vm.createContext({ prefs: { taskType, perm }, $state: v => v });
      vm.runInContext(initial, ctx);
      assert.equal(vm.runInContext('lPerm', ctx), taskType === 'sitebuild' && perm === 'full' ? 'auto' : perm);
    }
  }
  const ctx = vm.createContext({
    lTask: 'workspace', lPerm: 'full', lSite: '', lSites: [],
    activeConn: { kind: 'gcms' }, sites: [{ slug: 'example' }],
  });
  vm.runInContext(select, ctx);
  ctx.selectLauncherTask('sitebuild');
  assert.equal(ctx.lPerm, 'auto');
});
