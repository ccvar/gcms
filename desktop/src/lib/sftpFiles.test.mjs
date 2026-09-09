import { test } from 'node:test';
import assert from 'node:assert/strict';
import { copyTargetName, fitFileMenu } from './sftpFiles.ts';

test('copy to current directory preserves extensions and skips existing copies', () => {
  assert.equal(copyTargetName('site.caddy', false, new Set()), 'site.caddy');
  assert.equal(copyTargetName('site.caddy', false, new Set(['site.caddy'])), 'site 副本.caddy');
  assert.equal(copyTargetName('site.caddy', false, new Set(['site.caddy', 'site 副本.caddy'])), 'site 副本 2.caddy');
  assert.equal(copyTargetName('.env', false, new Set(['.env'])), '.env 副本');
  assert.equal(copyTargetName('conf.d', true, new Set(['conf.d'])), 'conf.d 副本');
  assert.equal(copyTargetName('notes', false, new Set(['notes'])), 'notes 副本');
});

test('tall folder context menu stays inside the viewport', () => {
  assert.deepEqual(fitFileMenu(950, 750, 240, 450, 1024, 768), { x: 776, y: 310 });
  assert.deepEqual(fitFileMenu(20, 30, 240, 450, 1024, 768), { x: 20, y: 30 });
  assert.deepEqual(fitFileMenu(0, 0, 240, 450, 240, 320), { x: 8, y: 8 });
});
