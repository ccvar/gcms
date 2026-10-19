import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { apiEndpointChanged, apiProfileInput, normalizeApiEndpoint, parseApiModels, validateApiProfileInput } from './apiProfiles.ts';

const profile = { id: 'p1', name: 'Work', protocol: 'openai_responses', base_url: 'https://api.openai.com/v1', enabled: true, models: ['model-a'], default_model: 'model-a', max_output_tokens: 8192, key_configured: true };

test('model IDs are trimmed, deduplicated and preserve ordered manual choices', () => {
  assert.deepEqual(parseApiModels(' model-a\nmodel-b,model-a， model-c\n'), ['model-a', 'model-b', 'model-c']);
  assert.deepEqual(parseApiModels(' \n'), []);
});

test('profile metadata allowlist never carries keys or UI credentials', () => {
  const input = apiProfileInput({ ...profile, apiKey: 'not-a-real-key', secret: 'not-a-real-secret' });
  assert.equal('apiKey' in input, false);
  assert.equal('secret' in input, false);
  assert.equal('key_configured' in input, false);
  assert.notEqual(input.models, profile.models);
});

test('endpoint and protocol changes need new consent, trailing slash does not', () => {
  assert.equal(normalizeApiEndpoint(' https://api.openai.com/v1/ '), profile.base_url);
  assert.equal(apiEndpointChanged(profile, { ...profile, base_url: `${profile.base_url}/` }), false);
  assert.equal(apiEndpointChanged(profile, { ...profile, base_url: 'https://other.example/v1' }), true);
  assert.equal(apiEndpointChanged(profile, { ...profile, protocol: 'openai_chat' }), true);
});

test('profile validation permits empty discovery setup but rejects unsafe URLs and invalid limits', () => {
  assert.equal(validateApiProfileInput(profile), '');
  assert.equal(validateApiProfileInput({ ...profile, models: [], default_model: '' }), '');
  for (const base_url of ['https://user:pass@example.com/v1', 'https://example.com/v1?key=abc', 'https://example.com/v1#key', 'http://example.com/v1', 'file:///api']) {
    assert.ok(validateApiProfileInput({ ...profile, base_url }));
  }
  for (const base_url of ['http://localhost:8080/v1', 'http://127.0.0.1:8080/v1', 'http://[::1]:8080/v1']) assert.equal(validateApiProfileInput({ ...profile, base_url }), '');
  for (const max_output_tokens of [0, -1, 1.5, NaN, 1_000_001]) assert.ok(validateApiProfileInput({ ...profile, max_output_tokens }));
  assert.ok(validateApiProfileInput({ ...profile, models: ['invalid model'] }));
  assert.ok(validateApiProfileInput({ ...profile, default_model: 'missing-model' }));
});

test('settings keeps keys transient, deletes through confirmation, and labels read-only testing honestly', () => {
  const source = fs.readFileSync(new URL('./ApiServices.svelte', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /localStorage|sessionStorage/);
  assert.match(source, /<ConfirmDialog[\s\S]*confirmText="删除连接"/);
  assert.match(source, /测试连接只读取模型列表/);
  assert.match(source, /original\?\.key_configured && endpointChanged && !apiKey\.trim\(\)/);
  assert.match(source, /apiKey = ''/);
  assert.match(source, /apiKey: apiKey\.trim\(\) \|\| null/);
});
