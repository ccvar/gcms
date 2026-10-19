import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resolveSftpDropTarget, localFileBasename } from './sftpDrop.ts';

const context = overrides => ({
  connId:'server-a', remoteDir:'/srv/uploads', busy:false, transferring:false,
  nativeDragging:false, insidePanel:true, ...overrides,
});

test('blank space, regular files and either kind of symlink use the current remote directory', () => {
  for (const hoveredRow of [
    undefined, null,
    { path:'/srv/uploads/file.txt', dir:false, link:false },
    { path:'/srv/uploads/link', dir:false, link:true },
    { path:'/srv/uploads/directory-link', dir:true, link:true },
  ]) assert.deepEqual(resolveSftpDropTarget(context({ hoveredRow })), { connId:'server-a', remoteDir:'/srv/uploads' });
});

test('a real directory row targets that directory, including the root and names with spaces', () => {
  for (const path of ['/srv/uploads/child', '/', '/srv/uploads/ 中文目录 ']) {
    assert.deepEqual(resolveSftpDropTarget(context({ hoveredRow:{ path, dir:true, link:false } })), { connId:'server-a', remoteDir:path });
  }
  assert.deepEqual(resolveSftpDropTarget(context({ remoteDir:'/' })), { connId:'server-a', remoteDir:'/' });
});

test('unavailable or busy destinations reject the drop without falling back to a different path', () => {
  for (const blocked of [
    { insidePanel:false }, { busy:true }, { transferring:true }, { nativeDragging:true },
    { targetDeleting:true }, { connId:'' }, { remoteDir:'' },
  ]) {
    assert.equal(resolveSftpDropTarget(context(blocked)), null);
    assert.equal(resolveSftpDropTarget(context({ hoveredRow:{ path:'/srv/child', dir:true, link:false }, ...blocked })), null);
  }
  for (const hoveredRow of [
    { dir:true, link:false }, { path:'', dir:true, link:false },
    { dir:false, link:false }, { path:'', dir:true, link:true },
  ]) assert.equal(resolveSftpDropTarget(context({ hoveredRow })), null);
});

test('accepted targets are frozen snapshots that cannot follow a later server or row change', () => {
  const row = { path:'/srv/uploads/child', dir:true, link:false };
  const state = context({ hoveredRow:row });
  const target = resolveSftpDropTarget(state);
  state.connId = 'server-b';
  state.remoteDir = '/other-server';
  row.path = '/other-server/child';
  assert.deepEqual(target, { connId:'server-a', remoteDir:'/srv/uploads/child' });
  assert.equal(Object.isFrozen(target), true);
  assert.throws(() => { target.connId = 'server-c'; }, TypeError);
});

test('local basenames handle POSIX files/directories and retain whitespace and Unicode', () => {
  for (const [path, expected] of [
    ['/Users/example/Desktop/report.txt', 'report.txt'],
    ['/Users/example/Desktop/folder/', 'folder'],
    ['/Users/example/Desktop/folder///', 'folder'],
    ['/Users/example/Desktop/ 数据备份 .zip ', ' 数据备份 .zip '],
    ['/tmp/file\\with\\backslashes.txt', 'file\\with\\backslashes.txt'],
    ['relative/name.txt', 'name.txt'],
    ['ordinary.txt', 'ordinary.txt'], [' ', ' '],
  ]) assert.equal(localFileBasename(path), expected, path);
});

test('local basenames handle Windows drive, mixed-separator and UNC paths', () => {
  for (const [path, expected] of [
    ['C:\\Users\\example\\Desktop\\report.txt', 'report.txt'],
    ['D:\\Backups\\folder\\', 'folder'],
    ['C:/Users/example\\文件夹/ report.txt ', ' report.txt '],
    ['\\\\server\\share\\folder\\file.txt', 'file.txt'],
    ['\\\\server\\share\\folder\\', 'folder'],
    ['//server/share/folder/file.txt', 'file.txt'],
    ['relative\\folder\\name.txt', 'name.txt'],
  ]) assert.equal(localFileBasename(path), expected, path);
});

test('empty paths, roots, drive-relative names and dot segments are not uploadable entries', () => {
  for (const path of [
    '', '/', '////', '\\', 'C:', 'C:\\', 'D:/', 'C:relative.txt',
    '\\\\server', '\\\\server\\share', '\\\\server\\share\\', '//server/share',
    '.', '..', './file', '../file', '/tmp/./file', '/tmp/../file', '/tmp/folder/.', '/tmp/folder/..',
    'C:\\folder\\.\\file', 'C:\\folder\\..\\file', '\\\\server\\share\\..\\file',
    '/tmp/invalid\0name',
  ]) assert.equal(localFileBasename(path), null, path);
});
