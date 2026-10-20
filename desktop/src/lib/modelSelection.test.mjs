import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import { normalizeApiEffort } from './apiReasoning.ts';

const source = fs.readFileSync(new URL('./modelSelection.ts', import.meta.url), 'utf8');
const parsed = ts.createSourceFile('modelSelection.ts', source, ts.ScriptTarget.ES2022, true);
const functions = parsed.statements.filter(ts.isFunctionDeclaration).map(node => node.getText(parsed).replace(/^export /, '')).join('\n');
const context = { normalizeApiEffort };
vm.createContext(context);
vm.runInContext(ts.transpileModule(functions, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
const options = [
  { value: 'claude::claude-opus-5-5' },
  { value: 'codex::gpt-6-sol' },
  { value: 'codex::gpt-6-luna' },
  { value: 'api:claude::claude-opus-5-5', apiProtocol: 'anthropic_messages' },
  { value: 'api:claude::claude-sonnet-5-5', apiProtocol: 'anthropic_messages' },
  { value: 'api:claude::claude-sonnet-4-6', apiProtocol: 'anthropic_messages' },
  { value: 'api:responses::gpt-6-sol', apiProtocol: 'openai_responses' },
  { value: 'api:chat::gpt-6-sol', apiProtocol: 'openai_chat' },
  { value: 'api:chat::custom-model', apiProtocol: 'openai_chat' },
];
const nextEffort = (previous, next, effort) => context.effortAfterModelPick(previous, next, effort, options);

test('entering and leaving API channels resets to provider default without changing CLI-only selection behavior', () => {
  assert.equal(nextEffort(options[0].value, options[3].value, 'high'), '');
  assert.equal(nextEffort(options[3].value, options[0].value, 'api:high'), '');
  assert.equal(nextEffort(options[1].value, options[2].value, 'ultra'), 'ultra');
});

test('same API protocol preserves valid efforts and clears unsupported settings', () => {
  assert.equal(nextEffort(options[3].value, options[4].value, 'api:max'), 'api:max');
  assert.equal(nextEffort(options[3].value, options[5].value, 'api:xhigh'), '');
  assert.equal(nextEffort(options[4].value, options[3].value, 'api:ultra'), '');
  assert.equal(nextEffort(options[7].value, options[8].value, 'api:high'), '');
  assert.equal(nextEffort('api:removed::gpt-6-sol', options[6].value, 'api:high'), '');
  assert.equal(nextEffort(options[3].value, options[4].value, 'high'), '', 'Legacy ignored effort is not activated');
});

test('changing API protocols does not reuse transport-specific efforts', () => {
  assert.equal(nextEffort(options[3].value, options[6].value, 'api:high'), '');
  assert.equal(nextEffort(options[6].value, options[7].value, 'api:none'), '');
});

test('task, managed and launch forms all normalize model switches before updating model identity', () => {
  const page = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
  for (const target of ['tf.effort', 'tf.fallbackEffort', 'mwEffort', 'mwFallbackEffort', 'lEffort']) {
    assert.ok(page.includes(`${target} = effortAfterModelPick(`), target);
  }
  assert.match(page, /value \? effortAfterModelPick\(`/);
  const managed = fs.readFileSync(new URL('./ManagedSettings.svelte', import.meta.url), 'utf8');
  assert.match(managed, /effort=effortAfterModelPick\(primary,v,effort,options\)/);
  assert.match(managed, /fallbackEffort=effortAfterModelPick\(fallback,v,fallbackEffort,options\)/);
});

test('launcher persists model identity and effort together before the first send', () => {
  const page = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
  const parsed = ts.createSourceFile('page.ts', page.slice(page.indexOf('>') + 1, page.indexOf('</script>')), ts.ScriptTarget.ES2022, true);
  const functions = parsed.statements.filter(node => ts.isFunctionDeclaration(node)
    && ['pickCombo', 'persistLauncherEffort'].includes(node.name?.text)).map(node => node.getText(parsed)).join('\n');
  const saved = [];
  const ctx = { lBrain: 'api:claude', lModel: 'claude-opus-5-5', lEffort: 'api:high', lFast: false,
    prefs: { brain: 'api:claude', model: 'claude-opus-5-5', effort: 'api:high' }, comboOpts: options,
    effortAfterModelPick: context.effortAfterModelPick, savePrefs: prefs => saved.push({ ...prefs }) };
  vm.createContext(ctx);
  vm.runInContext(ts.transpileModule(functions, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, ctx);
  ctx.pickCombo('api:responses::gpt-6-sol');
  assert.deepEqual(saved.at(-1), { brain: 'api:responses', model: 'gpt-6-sol', effort: '', fast: false });
  ctx.persistLauncherEffort('api:none');
  assert.deepEqual(saved.at(-1), { brain: 'api:responses', model: 'gpt-6-sol', effort: 'api:none', fast: false });
});
