import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

// Exercise the real event receiver; no model calls, real state or Tauri process.
const page = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
const script = page.slice(page.indexOf('>') + 1, page.indexOf('</script>'));
const parsed = ts.createSourceFile('page.ts', script, ts.ScriptTarget.ES2022, true);
const receiver = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'makeChannel');
assert.ok(receiver, 'makeChannel must remain available to exercise its actual behavior');
const code = ts.transpileModule(receiver.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;

function live() {
  return { text: '', renderedText: '', tools: [], activities: {}, lastActivityAt: 0, startedAt: 10, error: '', failed: false };
}

function harness() {
  let time = 100, scrolls = 0;
  const renders = [];
  const history = [{ role: 'user', text: 'earlier question' }, { role: 'assistant', text: 'earlier completed answer' }];
  const context = {
    lives: { active: live(), inactive: live() }, activeConvId: 'active',
    activeConv: { id: 'active', messages: history }, convos: [{ id: 'active', messages: history }],
    Channel: class {}, Date: { now: () => ++time },
    mdRender: text => { renders.push(text); return `<rendered>${text}</rendered>`; },
    scrollSoon: () => { scrolls += 1; },
  };
  vm.createContext(context); vm.runInContext(code, context);
  return { context, renders, history, scrolls: () => scrolls, channel: id => context.makeChannel(id) };
}

test('a safe text checkpoint removes failed partial text without duplicating prior responses', () => {
  const h = harness(), channel = h.channel('active');
  channel.onmessage({ type: 'tool', label: 'Read file', detail: 'earlier successful tool' });
  channel.onmessage({ type: 'activity', activity_id: 'earlier-tool', label: 'Completed tool', detail: 'kept', error: '', status: 'completed', kind: 'tool' });
  channel.onmessage({ type: 'delta', text: 'A' });
  const buf = h.context.lives.active;
  const tools = buf.tools, activities = buf.activities, history = JSON.stringify(h.history);
  channel.onmessage({ type: 'delta', text: 'failed partial response' });
  const previousActivity = buf.lastActivityAt, previousScrolls = h.scrolls();
  channel.onmessage({ type: 'text_reset', text: 'A' });
  assert.equal(buf.text, 'A');
  assert.equal(buf.renderedText, '<rendered>A</rendered>');
  assert.ok(buf.lastActivityAt > previousActivity);
  assert.equal(h.scrolls(), previousScrolls + 1);
  channel.onmessage({ type: 'delta', text: 'B' });
  assert.equal(buf.text, 'AB');
  assert.equal(buf.renderedText, '<rendered>AB</rendered>');
  assert.equal(buf.tools, tools);
  assert.equal(buf.activities, activities);
  assert.equal(buf.tools[0].detail, 'earlier successful tool');
  assert.equal(buf.activities['earlier-tool'].status, 'completed');
  assert.equal(JSON.stringify(h.history), history);
  assert.equal(h.context.activeConv.messages, h.history);
  assert.equal(h.context.convos[0].messages, h.history);
  assert.deepEqual(h.renders, ['A', 'Afailed partial response', 'A', 'AB']);
});

test('background-conversation resets update only its own buffer and never scroll the active view', () => {
  const h = harness(), active = h.channel('active'), inactive = h.channel('inactive');
  active.onmessage({ type: 'delta', text: 'visible answer' });
  const activeBefore = JSON.stringify(h.context.lives.active), scrolls = h.scrolls();
  inactive.onmessage({ type: 'delta', text: 'A' });
  inactive.onmessage({ type: 'tool', label: 'Background tool', detail: 'kept' });
  inactive.onmessage({ type: 'delta', text: 'failed partial' });
  inactive.onmessage({ type: 'text_reset', text: 'A' });
  inactive.onmessage({ type: 'delta', text: 'B' });
  assert.equal(h.context.lives.inactive.text, 'AB');
  assert.equal(h.context.lives.inactive.renderedText, '<rendered>AB</rendered>');
  assert.equal(h.context.lives.inactive.tools[0].detail, 'kept');
  assert.equal(JSON.stringify(h.context.lives.active), activeBefore);
  assert.equal(h.scrolls(), scrolls);
});

test('empty checkpoints clear only current streamed text, not tools or activity records', () => {
  const h = harness(), channel = h.channel('active');
  channel.onmessage({ type: 'tool', label: 'Preceding tool', detail: 'kept' });
  channel.onmessage({ type: 'activity', activity_id: 'reasoning', label: 'Thinking', detail: 'kept', error: '', status: 'completed', kind: 'reasoning' });
  channel.onmessage({ type: 'delta', text: 'failed first response' });
  channel.onmessage({ type: 'text_reset', text: '' });
  assert.equal(h.context.lives.active.text, '');
  assert.equal(h.context.lives.active.renderedText, '<rendered></rendered>');
  assert.equal(h.context.lives.active.tools.length, 1);
  assert.equal(h.context.lives.active.activities.reasoning.detail, 'kept');
  assert.equal(h.context.lives.active.startedAt, 10);
});

test('late reset packets for an ended conversation are ignored', () => {
  const h = harness(), channel = h.channel('inactive');
  delete h.context.lives.inactive;
  channel.onmessage({ type: 'text_reset', text: 'old checkpoint' });
  assert.equal(h.context.lives.inactive, undefined);
  assert.equal(h.renders.length, 0);
  assert.equal(h.scrolls(), 0);
});
