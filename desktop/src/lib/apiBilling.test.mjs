import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { billingPeriodLabel, formatBillingCost, formatBillingTime, formatBillingTokens, safeBillingUrl } from './apiBilling.ts';

test('missing or invalid costs are unavailable; genuine zero is explicitly zero', () => {
  for (const value of [null, undefined, NaN, Infinity, -Infinity]) assert.equal(formatBillingCost(value), '—');
  assert.equal(formatBillingCost(0), '$0.00');
  assert.equal(formatBillingCost(1.5), '$1.50');
  assert.equal(formatBillingCost(1234.56), '$1,234.56');
  assert.equal(formatBillingCost(0.0012), '$0.0012');
  assert.equal(formatBillingCost(0.00000001), '< $0.000001');
  assert.equal(formatBillingCost(-1.5), '-$1.50');
});

test('token formatter preserves exact integer count and distinguishes absent counts', () => {
  assert.equal(formatBillingTokens(0), '0');
  assert.equal(formatBillingTokens(1234567), '1,234,567');
  for (const value of [null, undefined, NaN, -1, 1.5, Infinity, Number.MAX_SAFE_INTEGER + 1]) assert.equal(formatBillingTokens(value), '—');
});

test('report timestamps and period boundaries are displayed in UTC', () => {
  assert.equal(formatBillingTime('2026-10-19T12:34:56Z'), '10-19 12:34 UTC');
  assert.equal(formatBillingTime('2026-10-19T20:34:56+08:00'), '10-19 12:34 UTC');
  for (const value of [null, undefined, '', 'not a time']) assert.equal(formatBillingTime(value), '—');
  assert.equal(billingPeriodLabel({ period_start: '2026-10-01T00:00:00Z', period_end: '2026-10-19T12:34:56Z' }), '2026-10-01 至 2026-10-19 12:34 UTC');
});

test('billing links are limited to the matching official provider and billing route', () => {
  assert.equal(safeBillingUrl({ provider: 'openai', billing_url: 'https://platform.openai.com/settings/organization/billing/overview' }), 'https://platform.openai.com/settings/organization/billing/overview');
  assert.equal(safeBillingUrl({ provider: 'anthropic', billing_url: 'https://platform.claude.com/settings/billing' }), 'https://platform.claude.com/settings/billing');
  for (const billing_url of ['https://evil.example/billing', 'javascript:alert(1)', 'http://platform.openai.com/settings/organization/billing', 'https://user:pass@platform.openai.com/settings/organization/billing', 'https://platform.openai.com/settings/organization/billing?secret=key', 'https://platform.openai.com/settings/organization/billing#secret', 'https://platform.claude.com/settings/billing']) assert.equal(safeBillingUrl({ provider: 'openai', billing_url }), null);
  assert.equal(safeBillingUrl({ provider: 'unsupported', billing_url: 'https://platform.claude.com/settings/billing' }), null);
});

test('billing settings keeps credentials transient, queries on demand and guards stale IPC results', () => {
  const source = fs.readFileSync(new URL('./ApiBilling.svelte', import.meta.url), 'utf8');
  const settings = fs.readFileSync(new URL('./ApiServices.svelte', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /localStorage|sessionStorage|console\./);
  assert.match(source, /type="password"/);
  assert.match(source, /onDestroy\([\s\S]*apiKey = ''/);
  assert.match(source, /generation === request && id === profileId/);
  assert.match(source, /confirmText="移除密钥"/);
  assert.match(source, /暂不支持查询/);
  assert.match(source, /组织账户 · UTC/);
  assert.match(source, /snapshot = null/);
  assert.match(settings, /#key `\$\{profile.id\}:\$\{profile.protocol\}:\$\{profile.base_url\}`/);
});
