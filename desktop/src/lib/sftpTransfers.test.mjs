import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  createTransfer, addTransfer, updateTransfer, finishTransfer, readTransferHistory, serializeTransferHistory,
  trimTransferHistory, isTransferActive, transferPercent, transferMetrics, formatTransferBytes,
  formatTransferDuration, transferStatusText, transferProgressText,
} from './sftpTransfers.ts';

const start = (id = 'transfer-a', options = {}) => createTransfer({
  id, connId: 'server-a', connectionName: 'Production', name: 'backup.tar.gz',
  direction: 'download', remotePath: '/srv/backup.tar.gz', ...options,
}, 1000);

test('add, progress and finish are immutable and keep transfer identities isolated', () => {
  const first = Object.freeze(start()), second = Object.freeze(start('transfer-b', { direction: 'upload' }));
  const original = Object.freeze([first]);
  const added = addTransfer(original, second);
  assert.deepEqual(added.map(record => record.id), ['transfer-b', 'transfer-a']);
  assert.equal(original.length, 1);
  const progressing = updateTransfer(added, first.id, { status: 'running', phase: 'downloading', bytes: 25, totalBytes: 100, files: 1, totalFiles: 4 }, 2000);
  assert.equal(progressing[0], second);
  assert.equal(progressing[1].bytes, 25);
  assert.equal(first.bytes, 0);
  const completed = finishTransfer(progressing, first.id, 'completed', { bytes: 100, files: 4, localPath: '/local/backup.tar.gz' }, 4000);
  assert.equal(completed[1].status, 'completed');
  assert.equal(completed[1].finishedAt, 4000);
  assert.equal(completed[1].localPath, '/local/backup.tar.gz');
  assert.equal(completed[1].cancelling, false);
  assert.equal(progressing[1].status, 'running');
});

test('late progress, duplicate starts and conflicting terminal events cannot revive or rewrite history', () => {
  for (const status of ['completed', 'failed', 'cancelled', 'interrupted']) {
    const history = finishTransfer([start()], 'transfer-a', status, { error: 'original detail' }, 2000);
    assert.equal(updateTransfer(history, 'transfer-a', { status: 'running', bytes: 80 }, 3000), history);
    assert.equal(finishTransfer(history, 'transfer-a', 'completed', {}, 3000), history);
    assert.equal(addTransfer(history, start()), history);
    assert.equal(updateTransfer(history, 'unknown', { bytes: 1 }), history);
    assert.equal(finishTransfer(history, 'unknown', 'failed'), history);
    assert.equal(history[0].error, 'original detail');
  }
});

test('progress ignores invalid counters and never moves time, bytes or status backwards', () => {
  const active = updateTransfer([start()], 'transfer-a', { status: 'running', bytes: 50, totalBytes: 100, files: 2, totalFiles: 4 }, 3000);
  const older = updateTransfer(active, 'transfer-a', { status: 'queued', bytes: 20, totalBytes: NaN, files: -5, totalFiles: Infinity }, 2000);
  assert.deepEqual(older[0], active[0]);
});

test('history retains every active transfer and only the latest 100 terminal records', () => {
  const records = Array.from({ length: 120 }, (_, i) => ({ ...start(`done-${i}`), status: 'completed', updatedAt: 2000 + i, finishedAt: 2000 + i }));
  const active = Array.from({ length: 110 }, (_, i) => start(`active-${i}`));
  const trimmed = trimTransferHistory([...records, ...active]);
  assert.equal(trimmed.length, 210);
  assert.equal(trimmed.filter(isTransferActive).length, 110);
  assert.equal(trimmed.some(record => record.id === 'done-19'), false);
  assert.equal(trimmed.some(record => record.id === 'done-20'), true);
  assert.equal(trimmed.some(record => record.id === 'done-119'), true);
  const finished = finishTransfer(trimmed, 'active-0', 'completed', {}, 5000);
  assert.equal(finished.length, 209);
  assert.equal(finished.some(record => record.id === 'done-20'), false);
  assert.equal(finished.some(record => record.id === 'active-0'), true);
});

test('persistence uses only known fields and never includes attached credentials or connection objects', () => {
  const record = {
    ...start(), localPath: '/Users/example/Downloads/backup.tar.gz',
    password: 'secret-password', token: 'private-token', connection: { password: 'nested-secret' },
  };
  const serialized = serializeTransferHistory([record]);
  assert.doesNotMatch(serialized, /secret-password|private-token|nested-secret|"password"|"connection"/);
  const parsed = JSON.parse(serialized);
  assert.equal(parsed[0].localPath, record.localPath);
  assert.equal(parsed[0].remotePath, record.remotePath);
  assert.equal(parsed[0].connectionName, 'Production');
});

test('reload marks queued and running records interrupted without replaying or losing completed history', () => {
  const queued = start('queued');
  const running = { ...start('running'), status: 'running', bytes: 30, cancelling: true };
  const completed = finishTransfer([start('done')], 'done', 'completed', { bytes: 100, totalBytes: 100 }, 2000)[0];
  const restored = readTransferHistory(serializeTransferHistory([queued, running, completed]), 5000);
  for (const record of restored.slice(0, 2)) {
    assert.equal(record.status, 'interrupted');
    assert.equal(record.finishedAt, 5000);
    assert.equal(record.cancelling, false);
    assert.match(record.error, /重新启动/);
  }
  assert.equal(restored[1].bytes, 30);
  assert.deepEqual(restored[2], completed);
  assert.deepEqual(readTransferHistory(serializeTransferHistory(restored), 9000), restored);
});

test('invalid persisted JSON and records are ignored and duplicate IDs are not replayed', () => {
  for (const raw of [null, undefined, '', '{broken', '{}', '42', 'null']) assert.deepEqual(readTransferHistory(raw), []);
  const source = JSON.stringify([null, {}, { ...start(), direction: 'sideways' }, { ...start(), status: 'unknown' }, start(), start()]);
  const restored = readTransferHistory(source, 3000);
  assert.equal(restored.length, 1);
  assert.equal(restored[0].status, 'interrupted');
});

test('percent and progress text support known totals, unknown totals and scanning', () => {
  assert.equal(transferPercent({ bytes: 6.1, totalBytes: 86 }), 7);
  assert.equal(transferPercent({ bytes: 100, totalBytes: 86 }), 100);
  assert.equal(transferPercent({ bytes: -5, totalBytes: 86 }), 0);
  assert.equal(transferPercent({ bytes: 0, totalBytes: 0 }), null);
  assert.equal(transferPercent({ bytes: 1, totalBytes: Infinity }), null);
  assert.equal(transferProgressText({ ...start(), bytes: 1024, totalBytes: 4096, files: 1, totalFiles: 3 }), '1 KB / 4 KB · 1/3 个文件');
  assert.equal(transferProgressText({ ...start(), status: 'running', phase: 'scanning', totalFiles: 12 }), '已发现 12 个文件');
  assert.equal(transferProgressText(start()), '');
});

test('speed and ETA appear only with usable elapsed transfer data', () => {
  const active = { ...start(), status: 'running', phase: 'downloading', bytes: 2048, totalBytes: 8192, updatedAt: 3000 };
  assert.deepEqual(transferMetrics(active), { bytesPerSecond: 1024, etaSeconds: 6 });
  assert.deepEqual(transferMetrics({ ...active, totalBytes: 0 }), { bytesPerSecond: 1024, etaSeconds: null });
  for (const patch of [
    { status: 'queued' }, { status: 'completed' }, { phase: 'scanning' }, { phase: 'dragging' },
    { updatedAt: 1500 }, { updatedAt: 1000 }, { bytes: 0 }, { bytes: NaN }, { cancelling: true },
  ]) assert.deepEqual(transferMetrics({ ...active, ...patch }), { bytesPerSecond: null, etaSeconds: null });
});

test('formatting labels upload/download status and avoids non-finite numbers', () => {
  assert.equal(formatTransferBytes(6.1 * 1024 ** 2), '6.1 MB');
  assert.equal(formatTransferBytes(Infinity), '0 B');
  assert.equal(formatTransferBytes(-1), '0 B');
  assert.equal(formatTransferDuration(0), '0 秒');
  assert.equal(formatTransferDuration(61), '2 分钟');
  assert.equal(formatTransferDuration(3601), '1 小时 1 分钟');
  assert.equal(formatTransferDuration(NaN), '');
  assert.equal(transferStatusText(start()), '等待中');
  assert.equal(transferStatusText({ ...start(), status: 'running', phase: 'uploading', direction: 'upload' }), '上传中');
  assert.equal(transferStatusText({ ...start(), status: 'running', phase: 'downloading' }), '下载中');
  assert.equal(transferStatusText({ ...start(), cancelling: true }), '正在取消');
  assert.equal(transferStatusText({ ...start(), status: 'failed', cancelling: true }), '失败');
});
