import { test } from 'node:test';
import assert from 'node:assert/strict';
import capabilities from './apiReasoningCapabilities.json' with { type: 'json' };
import { apiEffortValue, apiReasoningLevels, apiReasoningModelMatches, normalizeApiEffort } from './apiReasoning.ts';

test('screenshot Claude models share the verified five API effort levels', () => {
  for (const model of ['claude-opus-5-5', 'claude-fable-5-1', 'claude-sonnet-5-5']) {
    assert.deepEqual(apiReasoningLevels('anthropic_messages', model), ['low', 'medium', 'high', 'xhigh', 'max']);
  }
  assert.deepEqual(apiReasoningLevels('anthropic_messages', 'claude-sonnet-4-6'), ['low', 'medium', 'high', 'max']);
});

test('OpenAI models and protocols expose only supported effort tiers, never CLI ultra', () => {
  assert.deepEqual(apiReasoningLevels('openai_responses', 'gpt-6-sol'), ['none', 'low', 'medium', 'high', 'xhigh', 'max']);
  assert.deepEqual(apiReasoningLevels('openai_responses', 'gpt-6.1-sol'), ['low', 'medium', 'high', 'xhigh', 'max']);
  assert.deepEqual(apiReasoningLevels('openai_responses', 'gpt-5.5'), ['none', 'low', 'medium', 'high', 'xhigh']);
  assert.deepEqual(apiReasoningLevels('openai_chat', 'gpt-6-sol'), ['none']);
  assert.deepEqual(apiReasoningLevels('openai_chat', 'gpt-6-astra'), []);
  for (const entry of capabilities) assert.ok(!entry.levels.includes('ultra'));
});

test('matching accepts exact IDs and dated snapshots but not lookalike or future models', () => {
  assert.equal(apiReasoningModelMatches('gpt-5.5-2026-04-23', 'gpt-5.5'), true);
  assert.equal(apiReasoningModelMatches('claude-opus-4-6-20260205', 'claude-opus-4-6'), true);
  for (const model of ['gpt-5.50', 'gpt-5.5-pro', 'gpt-5.5-custom', 'vendor/gpt-5.5', 'gpt-5.5-2026-04-23-extra']) {
    assert.equal(apiReasoningModelMatches(model, 'gpt-5.5'), false);
  }
  assert.deepEqual(apiReasoningLevels('openai_responses', 'gpt-future'), []);
  assert.deepEqual(apiReasoningLevels('openai_chat', 'claude-sonnet-5-5'), []);
  assert.deepEqual(apiReasoningLevels(undefined, 'claude-sonnet-5-5'), []);
});

test('model changes preserve only valid effort values and default remains empty', () => {
  assert.equal(normalizeApiEffort('anthropic_messages', 'claude-sonnet-5-5', 'api:max'), 'api:max');
  assert.equal(normalizeApiEffort('anthropic_messages', 'claude-sonnet-5-5', 'api:none'), '');
  assert.equal(normalizeApiEffort('openai_responses', 'gpt-6-sol', 'api:ultra'), '');
  assert.equal(normalizeApiEffort('openai_chat', 'custom-model', 'api:high'), '');
  assert.equal(normalizeApiEffort('anthropic_messages', 'claude-opus-5-5', ''), '');
});

test('legacy hidden CLI effort stays at provider defaults until explicitly selected', () => {
  for (const value of ['low', 'medium', 'high', 'max', 'ultra']) {
    assert.equal(apiEffortValue(value), '');
    assert.equal(normalizeApiEffort('anthropic_messages', 'claude-sonnet-5-5', value), '');
  }
  assert.equal(apiEffortValue('api:high'), 'high');
  assert.equal(normalizeApiEffort('anthropic_messages', 'claude-sonnet-5-5', 'api:high'), 'api:high');
});

test('shared catalog has unambiguous entries and callers cannot mutate it', () => {
  const seen = new Set();
  for (const entry of capabilities) {
    assert.equal(new Set(entry.levels).size, entry.levels.length);
    assert.ok(entry.levels.length);
    for (const model of entry.models) {
      const key = `${entry.protocol}:${model}`;
      assert.ok(!seen.has(key), key);
      seen.add(key);
    }
  }
  apiReasoningLevels('anthropic_messages', 'claude-sonnet-5-5').push('ultra');
  assert.ok(!apiReasoningLevels('anthropic_messages', 'claude-sonnet-5-5').includes('ultra'));
});
