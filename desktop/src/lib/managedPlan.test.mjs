import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import { canApplyPlan, planElapsed, MANAGED_PLAN_TIMEOUT_SECONDS } from './managedPlan.ts';

// Exercise the actual wizard entry point and event receiver with mocked IPC,
// without starting a model, approving an operation or reaching a real server.
const source = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
function declaration(start, end) { return source.slice(source.indexOf(start), source.indexOf(end, source.indexOf(start))); }
const code = ts.transpileModule([
  declaration('  function stopMwPlan(', '  async function closeManagedWizard('),
  declaration('  async function mwGenPlan(', '  async function mwRefreshPromptDefaults('),
  declaration('  function makeChannel(', '  function optimisticUser('),
  declaration('  async function respondMwPlanPermit(', '  $effect(() => {\n    if (mwGenBusy'),
].join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;

function harness() {
  let resolve, reject, clock = 100_000;
  const result = new Promise((a, b) => { resolve = a; reject = b; });
  const calls = [], timers = new Set();
  const context = {
    mwSite: 'example', mwPlan: '', mwMode: 'plan', mwOpen: true, mwBrain: 'claude', mwModel: 'test-model', mwEffort: '',
    mwGenBusy: false, mwGenRun: null, mwGenError: '', mwGenNotice: '', mwPermitError: '', mwPermitSending: [],
    mwPlanPermits: [], pendingPermits: [], respondedPermits: new Set(),
    sites: [{ slug: 'example', name: 'Example' }], activeConnId: 'windows-user', activeConn: { name: 'Windows fixture' }, activeConvId: '',
    convos: [], lives: {}, running: {}, crypto: { randomUUID: () => 'plan-1' },
    Date: { now: () => clock }, MANAGED_PLAN_TIMEOUT_SECONDS, canApplyPlan, MW_PLAN_PROMPT: '计划测试',
    brainUsable: () => true, optimisticUser: text => ({ role: 'user', text }), say: () => {},
    mdRender: text => text, scrollSoon: () => {}, Channel: class {},
    clearStructuredGcmsUnlock: () => {},
    setInterval: fn => { timers.add(fn); return fn; }, clearInterval: fn => timers.delete(fn),
    invoke: async (command, args) => { calls.push({ command, args }); return command === 'start_conversation' ? result : false; },
    endTurn: (_conv, id) => { delete context.running[id]; delete context.lives[id]; },
    failTurn: async (_error, id) => { delete context.running[id]; delete context.lives[id]; },
    refreshConvos: () => new Promise(() => {}), // slow history must not block completion
  };
  vm.createContext(context); vm.runInContext(code, context);
  return {
    context, calls, timers, resolve: (text = '完整计划') => resolve({ ...context.convos[0], messages: [{ role: 'assistant', text, error: false }] }), reject,
    tick: async (elapsed = 1000) => { clock += elapsed; for (const fn of timers) fn(); await new Promise(resolve => setImmediate(resolve)); },
  };
}

test('background plan registers progress and permission polling before invoking the model', async () => {
  const h = harness(), task = h.context.mwGenPlan();
  assert.equal(h.context.running['plan-1'], 'windows-user');
  assert.equal(h.context.convos[0].status, 'running');
  const args = h.calls[0].args;
  assert.equal(args.permMode, 'auto');
  assert.equal(args.timeoutSeconds, 480);
  args.onEvent.onmessage({ type: 'tool', label: '读取资料', detail: 'C:\\Users\\测试 用户\\site.json' });
  args.onEvent.onmessage({ type: 'delta', text: '部分输出' });
  assert.equal(h.context.lives['plan-1'].text, '部分输出');
  assert.equal(h.context.lives['plan-1'].tools.length, 1);
  h.resolve(); await task;
  assert.equal(h.context.mwPlan, '完整计划');
  assert.equal(h.context.mwGenBusy, false);
  assert.equal(h.timers.size, 0);
});

test('stop retries across slow startup; false cancellation does not release the spinner early', async () => {
  const h = harness(), task = h.context.mwGenPlan();
  h.context.stopMwPlan();
  await h.tick(); await h.tick();
  assert.equal(h.calls.filter(x => x.command === 'cancel_turn').length, 2);
  assert.equal(h.context.mwGenBusy, true);
  h.resolve('不完整输出'); await task;
  assert.equal(h.context.mwPlan, '');
  assert.equal(h.context.mwGenBusy, false);
});

test('timeout requests cancellation, errors are visible and unrelated chats remain registered', async () => {
  const h = harness(); h.context.running.other = 'another-connection';
  const task = h.context.mwGenPlan();
  await h.tick(480_001);
  assert.match(h.context.mwGenRun.stopReason, /8 分钟/);
  assert.equal(h.calls.at(-1).command, 'cancel_turn');
  h.reject(new Error('模型连接超时')); await task;
  assert.match(h.context.mwGenError, /模型连接超时/);
  assert.equal(h.context.running.other, 'another-connection');
  assert.equal(h.context.running['plan-1'], undefined);
});

test('editing, closing, switching sites/connections or detaching prevents stale writeback', async () => {
  for (const change of [c => { c.mwPlan = '手写计划'; }, c => { c.mwOpen = false; }, c => { c.mwSite = 'other'; }, c => { c.activeConnId = 'other'; }, c => { c.mwGenRun.apply = false; }]) {
    const h = harness(), task = h.context.mwGenPlan();
    change(h.context); const before = h.context.mwPlan;
    h.resolve(); await task;
    assert.equal(h.context.mwPlan, before);
  }
});

test('failed model events never turn partial prose into a successful plan', async () => {
  const h = harness(), task = h.context.mwGenPlan();
  h.calls[0].args.onEvent.onmessage({ type: 'done', ok: false, error: '额度不足' });
  h.resolve('部分计划'); await task;
  assert.equal(h.context.mwPlan, '');
  assert.equal(h.context.mwGenError, '额度不足');
});

test('approval delivery failure keeps the request visible and retryable', async () => {
  const h = harness();
  h.context.mwPlanPermits = h.context.pendingPermits = [{ id: 'permit', conv: 'plan-1' }];
  h.context.invoke = async () => { throw new Error('IPC error'); };
  await h.context.respondMwPlanPermit('permit', true);
  assert.equal(h.context.pendingPermits.length, 1);
  assert.equal(h.context.respondedPermits.size, 0);
  assert.match(h.context.mwPermitError, /确认未送达/);
  h.context.invoke = async () => {};
  await h.context.respondMwPlanPermit('permit', false);
  assert.equal(h.context.pendingPermits.length, 0);
  assert.ok(h.context.respondedPermits.has('permit'));
});

test('elapsed time handles wall clock drift safely', () => {
  assert.equal(planElapsed(1000, 0), '0 分 0 秒');
  assert.equal(planElapsed(0, 91_000), '1 分 31 秒');
});
