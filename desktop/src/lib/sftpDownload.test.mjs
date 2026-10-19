import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import * as transferHistory from './sftpTransfers.ts';
import { resolveSftpDropTarget, localFileBasename } from './sftpDrop.ts';

// Exercise the actual page handlers with mocked dialogs and IPC. These tests
// never open a native dialog, start an OS drag, or read/write a remote file.
const page = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
const script = page.slice(page.indexOf('>') + 1, page.indexOf('</script>'));
const source = ts.createSourceFile('page.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
function handlers(names) {
  const functions = names.map(name => {
    const node = source.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
    assert.ok(node, `Missing page handler: ${name}`);
    return node.getText(source);
  });
  return ts.transpileModule(functions.join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
}

const file = { path: '/config/site.json', name: 'site.json', dir: false, link: false, size: 24, mtime: 1700000000, depth: 0 };
const folder = { path: '/config/assets', name: 'assets', dir: true, link: false, size: 0, mtime: 1700000001, depth: 0 };
const plain = value => JSON.parse(JSON.stringify(value));
class MockNode {
  constructor(parent = null) { this.parentNode = parent; }
  contains(node) {
    for (let current = node; current; current = current.parentNode) if (current === this) return true;
    return false;
  }
}
class MockElement extends MockNode {
  constructor(parent = null, path) { super(parent); this.dataset = path === undefined ? {} : { sftpDropPath:path }; }
  closest() { return this.dataset.sftpDropPath !== undefined ? this : this.parentNode?.closest?.() ?? null; }
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
async function until(predicate) {
  for (let i = 0; i < 20 && !predicate(); i++) await Promise.resolve();
  assert.ok(predicate(), 'Expected mocked async operation to start');
}
function harness(overrides = {}) {
  const calls = [], dialogs = [], messages = [], reloads = [], reveals = [], attached = [], paneAttachments = [];
  const storage = new Map(), timers = new Map();
  let serial = 0;
  let timerSerial = 0;
  const ctx = {
    ...transferHistory,
    resolveSftpDropTarget, localFileBasename,
    Node:MockNode, Element:MockElement, isMac:true,
    sftpConnId: 'server-a', sftpPath: '/config', sftpRows: [file, folder], sftpSel: '',
    sftpBusy: false, sftpXfer: '', sftpNativeDragging: false,
    sftpDeleting: new Set(), sftpDownloadTask: null, sftpUploadTask: null,
    sftpDropCapture:null, sftpDropTarget:null, sftpFileOver:false, composerFileOver:false,
    sftpTransfers: [], sftpHistoryTimer: undefined,
    sftpHistoryKey: `${transferHistory.SFTP_TRANSFER_HISTORY_KEY}:test-window`,
    conns: [{ id:'server-a', name:'Server A', remark:' Production ', password:'never-persist-this' }, { id:'server-b', name:'Server B' }],
    localStorage: { getItem:key => storage.get(key) ?? null, setItem:(key, value) => storage.set(key, value) },
    setTimeout:callback => { const id=++timerSerial; timers.set(id, callback); return id; },
    clearTimeout:id => timers.delete(id),
    loadDir:async (path, force) => reloads.push({ path, force }),
    revealItemInDir:async path => reveals.push(path),
    attachFile:async file => attached.push(file),
    splitConversation:id => id === 'chat-b' ? { id } : null,
    splitUpload:async (conversation, file) => paneAttachments.push({ id:conversation.id, file }),
    splitErrors:{},
    btoa,
    fctxMenu: { row: file },
    crypto: { randomUUID: () => `transfer-${++serial}` },
    Channel: class { onmessage = () => {}; },
    open: async options => { dialogs.push({ kind: 'open', options }); return '/local/downloads'; },
    saveDialog: async options => { dialogs.push({ kind: 'save', options }); return '/local/site.json'; },
    invoke: async (command, args) => {
      calls.push({ command, args });
      return { path: '/local/downloads/site.json', drag_id: null, skipped: 0 };
    },
    say: (message, kind) => messages.push({ message, kind }),
    ...overrides,
  };
  vm.createContext(ctx);
  vm.runInContext(handlers([
    'sftpDeletingKey', 'isSftpDeleting', 'sftpDownloadSelected', 'sftpDownload', 'cancelSftpDownload', 'onSftpDragStart',
    'loadSftpTransfers', 'persistSftpTransfers', 'addSftpTransfer', 'updateSftpTransfer', 'progressSftpTransfer',
    'finishSftpTransfer', 'clearSftpTransfers', 'revealSftpTransfer', 'cancelSftpTransfer',
    'sftpJoin', 'bytesToB64', 'sftpUpload', 'uploadSftpFile', 'sftpUploadDropped',
    'beginSftpLocalDrop', 'discardSftpLocalDrop', 'sftpDropTargetAt', 'localDropEntries',
    'onSftpDragOver', 'onSftpDragLeave', 'onSftpDrop', 'onComposerDrop', 'onComposerDragOver', 'onComposerDragLeave',
    'uploadPaneFiles', 'onComposerPaste',
  ]), ctx);
  return { ctx, calls, dialogs, messages, storage, timers, reloads, reveals, attached, paneAttachments };
}
function dragEvent() {
  return {
    prevented: false, stopped: false,
    preventDefault() { this.prevented = true; },
    stopPropagation() { this.stopped = true; },
  };
}
function dropEvent(files, directories = files.map(() => false), hoveredPath) {
  const panel = new MockElement();
  const row = hoveredPath === undefined ? panel : new MockElement(panel, hoveredPath);
  return {
    ...dragEvent(), currentTarget:panel, target:new MockElement(row), relatedTarget:null,
    dataTransfer:{
      types:['Files'], files,
      items:files.map((file, index) => ({ kind:'file', webkitGetAsEntry:() => ({ isDirectory:directories[index] }) })),
    },
  };
}

test('download toolbar resolves the selected current file or directory row', async () => {
  for (const selected of [file, folder]) {
    const { ctx } = harness({ sftpSel: selected.path });
    const downloaded = [];
    ctx.sftpDownload = row => { downloaded.push(row); };
    await ctx.sftpDownloadSelected();
    assert.deepEqual(downloaded, [selected]);
  }
});

test('download toolbar ignores missing selections and conflicting operations', async () => {
  const cases = [
    { sftpSel: '' },
    { sftpSel: '/config/no-longer-visible.json' },
    { sftpBusy: true },
    { sftpXfer: '上传中…' },
    { sftpNativeDragging: true },
    { sftpDeleting: new Set(['server-a\n/config/site.json']) },
    { sftpDeleting: new Set(['server-a\n/config']) },
  ];
  for (const state of cases) {
    const { ctx } = harness({ sftpSel: file.path, ...state });
    let downloads = 0;
    ctx.sftpDownload = () => { downloads++; };
    await ctx.sftpDownloadSelected();
    assert.equal(downloads, 0, JSON.stringify(plain(state)));
  }
});

test('direct download guards busy, active transfers, dragging, and deleting ancestors', async () => {
  for (const state of [
    { sftpBusy: true }, { sftpXfer: '上传中…' }, { sftpNativeDragging: true },
    { sftpDeleting: new Set(['server-a\n/config/site.json']) },
    { sftpDeleting: new Set(['server-a\n/config']) },
  ]) {
    const { ctx, calls, dialogs } = harness(state);
    const menu = ctx.fctxMenu;
    await ctx.sftpDownload(file);
    assert.equal(calls.length, 0);
    assert.equal(dialogs.length, 0);
    assert.equal(ctx.fctxMenu, menu);
    assert.equal(ctx.sftpSel, '');
  }
});

test('cancelling either save dialog leaves no download or stale transfer state', async () => {
  for (const row of [file, folder]) {
    const { ctx, calls, messages } = harness({ open: async () => null, saveDialog: async () => null });
    await ctx.sftpDownload(row);
    assert.equal(calls.length, 0);
    assert.equal(messages.length, 0);
    assert.equal(ctx.sftpXfer, '');
    assert.equal(ctx.sftpDownloadTask, null);
    assert.equal(ctx.fctxMenu, null);
    assert.equal(ctx.sftpSel, row.path);
    assert.equal(ctx.sftpTransfers.length, 0, 'A cancelled destination dialog is not a transfer');
  }
});

test('plain downloads use the selected destination and finish with clean progress state', async () => {
  const { ctx, calls, dialogs, messages, storage } = harness({ sftpDeleting: new Set(['server-b\n/config']) });
  await ctx.sftpDownload(file);
  assert.equal(dialogs.length, 1);
  assert.equal(dialogs[0].kind, 'save');
  assert.equal(dialogs[0].options.defaultPath, file.name);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].command, 'sftp_download_entry');
  const { onProgress, ...args } = calls[0].args;
  assert.equal(args.connId, 'server-a');
  assert.equal(args.remote, file.path);
  assert.equal(args.local, '/local/site.json');
  assert.equal(args.directory, false);
  assert.equal(args.archive, false);
  assert.equal(args.transferId, 'transfer-1');
  assert.equal(typeof onProgress.onmessage, 'function');
  assert.equal(ctx.fctxMenu, null);
  assert.equal(ctx.sftpSel, file.path);
  assert.equal(ctx.sftpXfer, '');
  assert.equal(ctx.sftpDownloadTask, null);
  assert.equal(ctx.sftpTransfers[0].status, 'completed');
  assert.equal(ctx.sftpTransfers[0].localPath, '/local/downloads/site.json');
  assert.equal(ctx.sftpTransfers[0].connectionName, 'Production');
  assert.doesNotMatch(storage.get(ctx.sftpHistoryKey), /never-persist-this|password/);
  assert.equal(storage.has(transferHistory.SFTP_TRANSFER_HISTORY_KEY), false);
  assert.match(messages[0].message, /已下载到 \/local\/downloads\/site\.json/);
  onProgress.onmessage({ phase: 'download', bytes: 99 });
  assert.equal(ctx.sftpDownloadTask, null, 'Late progress must not restore a completed task');
  assert.equal(ctx.sftpTransfers[0].status, 'completed');
});

test('directory download picks a directory, while archives and symlinks use a file save dialog', async () => {
  for (const { row, archive, kind, directory, defaultPath } of [
    { row: folder, archive: false, kind: 'open', directory: true },
    { row: folder, archive: true, kind: 'save', directory: true, defaultPath: 'assets.tar.gz' },
    { row: { ...folder, link: true }, archive: false, kind: 'save', directory: false, defaultPath: 'assets' },
  ]) {
    const { ctx, calls, dialogs } = harness();
    await ctx.sftpDownload(row, archive);
    assert.equal(dialogs.length, 1);
    assert.equal(dialogs[0].kind, kind);
    if (kind === 'open') {
      assert.equal(dialogs[0].options.directory, true);
      assert.equal(dialogs[0].options.multiple, false);
    } else assert.equal(dialogs[0].options.defaultPath, defaultPath);
    assert.equal(calls[0].args.directory, directory);
    assert.equal(calls[0].args.archive, archive);
    assert.equal(calls[0].args.local, kind === 'open' ? '/local/downloads' : '/local/site.json');
  }
});

test('switching servers during the destination dialog retains the source connection', async () => {
  const destination = deferred();
  const { ctx, calls } = harness({ saveDialog: () => destination.promise });
  const pending = ctx.sftpDownload(file);
  assert.equal(ctx.fctxMenu, null);
  assert.equal(ctx.sftpSel, file.path);
  assert.ok(ctx.sftpXfer);
  ctx.sftpConnId = 'server-b';
  destination.resolve('/local/chosen.json');
  await pending;
  assert.equal(calls.length, 1);
  assert.equal(calls[0].args.connId, 'server-a');
  assert.equal(calls[0].args.remote, file.path);
  assert.equal(calls[0].args.local, '/local/chosen.json');
});

test('progress only updates its matching transfer and enables one cancellation request', async () => {
  const download = deferred();
  const { ctx, calls } = harness();
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    if (command === 'sftp_download_entry') return download.promise;
  };
  const pending = ctx.sftpDownload(file);
  await until(() => calls.length === 1);
  assert.equal(ctx.sftpDownloadTask.started, false);
  await ctx.cancelSftpDownload();
  assert.equal(calls.length, 1, 'Cancellation waits for the backend to start');
  const progress = { phase: 'download', bytes: 12, total_bytes: 24, files: 0, total_files: 1, skipped: 0 };
  calls[0].args.onProgress.onmessage(progress);
  assert.equal(ctx.sftpDownloadTask.started, true);
  assert.deepEqual(plain(ctx.sftpDownloadTask.progress), progress);
  await ctx.cancelSftpDownload();
  await ctx.cancelSftpDownload();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].command, 'sftp_cancel_download');
  assert.deepEqual(plain(calls[1].args), { transferId: calls[0].args.transferId });
  assert.equal(ctx.sftpDownloadTask.cancelling, true);
  const currentTask = ctx.sftpDownloadTask;
  const otherTask = { id: 'other-transfer', started: false, progress: null, cancelling: false };
  ctx.sftpDownloadTask = otherTask;
  calls[0].args.onProgress.onmessage({ ...progress, bytes: 24 });
  assert.equal(ctx.sftpDownloadTask, otherTask);
  ctx.sftpDownloadTask = currentTask;
  download.resolve({ path: '/local/site.json', drag_id: null, skipped: 0 });
  await pending;
  assert.equal(ctx.sftpXfer, '');
  assert.equal(ctx.sftpDownloadTask, null);
});

test('failed cancellation can be retried and a failed download clears its busy state', async () => {
  const download = deferred();
  const { ctx, calls, messages } = harness();
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    if (command === 'sftp_download_entry') return download.promise;
    throw Error('cancel unavailable');
  };
  const pending = ctx.sftpDownload(file);
  await until(() => calls.length === 1);
  calls[0].args.onProgress.onmessage({ phase: 'download', bytes: 1 });
  await ctx.cancelSftpDownload();
  assert.equal(ctx.sftpDownloadTask.cancelling, false);
  assert.match(messages[0].message, /cancel unavailable/);
  assert.equal(messages[0].kind, 'err');
  download.reject(Error('connection lost'));
  await pending;
  assert.match(messages.at(-1).message, /connection lost/);
  assert.equal(messages.at(-1).kind, 'err');
  assert.equal(ctx.sftpXfer, '');
  assert.equal(ctx.sftpDownloadTask, null);
});

test('one file or directory drag immediately starts a native download without preparation or a dialog', async () => {
  for (const row of [file, folder]) {
    const { ctx, calls, dialogs } = harness();
    const event = dragEvent();
    await ctx.onSftpDragStart(event, row);
    assert.equal(event.prevented, true);
    assert.equal(event.stopped, true);
    assert.equal(dialogs.length, 0);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].command, 'sftp_drag_download');
    const { onProgress, onEvent, ...args } = calls[0].args;
    assert.deepEqual(plain(args), {
      connId: 'server-a', remote: row.path, directory: row.dir,
      transferId: 'transfer-1',
    });
    assert.equal(typeof onProgress.onmessage, 'function');
    assert.equal(typeof onEvent.onmessage, 'function');
    assert.equal(ctx.sftpNativeDragging, true);
    assert.ok(ctx.sftpXfer);
    assert.equal(ctx.sftpDownloadTask.drag, true);
    assert.equal(ctx.sftpTransfers[0].status, 'queued');
    assert.equal(ctx.fctxMenu, null);
    assert.equal(ctx.sftpSel, row.path);
    await ctx.onSftpDragStart(dragEvent(), row);
    assert.equal(calls.length, 1, 'A second drag cannot create a duplicate transfer');
    onEvent.onmessage({ phase: 'cancelled' });
  }
});

test('native drag ignores entries while the list or another file operation is busy', async () => {
  for (const state of [
    { sftpBusy: true }, { sftpXfer: '上传中…' }, { sftpNativeDragging: true },
    { sftpDeleting: new Set(['server-a\n/config']) },
  ]) {
    const { ctx, calls, dialogs } = harness(state);
    await ctx.onSftpDragStart(dragEvent(), file);
    assert.equal(calls.length, 0);
    assert.equal(dialogs.length, 0);
    assert.equal(ctx.sftpSel, '');
  }
});

test('native drag keeps its original connection and remains busy while the drop downloads', async () => {
  const { ctx, calls, messages } = harness();
  await ctx.onSftpDragStart(dragEvent(), file);
  const { onEvent, onProgress, connId } = calls[0].args;
  ctx.sftpConnId = 'server-b';
  onEvent.onmessage({ phase: 'dragging' });
  assert.equal(ctx.sftpNativeDragging, true);
  onEvent.onmessage({ phase: 'dropped', path: '/local/desktop/site.json' });
  assert.equal(connId, 'server-a');
  assert.equal(ctx.sftpNativeDragging, false);
  assert.ok(ctx.sftpXfer, 'The transfer stays busy after releasing the mouse');
  assert.equal(ctx.sftpDownloadTask.id, 'transfer-1');
  assert.equal(ctx.sftpTransfers[0].status, 'running');
  assert.equal(ctx.sftpTransfers[0].connId, 'server-a');
  const progress = { phase: 'download', bytes: 12, total_bytes: 24, files: 0, total_files: 1, skipped: 0 };
  onProgress.onmessage(progress);
  assert.deepEqual(plain(ctx.sftpDownloadTask.progress), progress);
  assert.equal(ctx.sftpDownloadTask.started, true);
  onEvent.onmessage({ phase: 'completed', path: '/local/desktop/site.json' });
  assert.equal(ctx.sftpNativeDragging, false);
  assert.equal(ctx.sftpXfer, '');
  assert.equal(ctx.sftpDownloadTask, null);
  assert.equal(ctx.sftpTransfers[0].status, 'completed');
  assert.equal(ctx.sftpTransfers[0].localPath, '/local/desktop/site.json');
  assert.ok(messages.some(({ message }) => message.includes('/local/desktop/site.json')));
});

test('late drag progress and completion cannot change a newer transfer', async () => {
  const { ctx, calls, messages } = harness();
  await ctx.onSftpDragStart(dragEvent(), file);
  const { onEvent, onProgress } = calls[0].args;
  const newer = { id: 'new-transfer', drag: true, started: false, cancelling: false, progress: null };
  ctx.sftpDownloadTask = newer;
  ctx.sftpXfer = 'new download';
  ctx.sftpNativeDragging = true;
  const priorMessages = messages.length;
  onProgress.onmessage({ phase: 'download', bytes: 24 });
  for (const phase of ['dropped', 'completed', 'cancelled', 'error']) {
    onEvent.onmessage({ phase, path: '/local/old.json', error: 'old failure' });
    assert.equal(ctx.sftpDownloadTask, newer);
    assert.equal(ctx.sftpXfer, 'new download');
    assert.equal(ctx.sftpNativeDragging, true);
  }
  assert.equal(messages.length, priorMessages);
});

test('cancelling the native drop clears transfer state without an error toast', async () => {
  const { ctx, calls, messages } = harness();
  await ctx.onSftpDragStart(dragEvent(), folder);
  calls[0].args.onEvent.onmessage({ phase: 'cancelled' });
  assert.equal(ctx.sftpNativeDragging, false);
  assert.equal(ctx.sftpXfer, '');
  assert.equal(ctx.sftpDownloadTask, null);
  assert.equal(messages.filter(({ kind }) => kind === 'err').length, 0);
});

test('a dropped native transfer can be cancelled before its first progress update', async () => {
  const { ctx, calls, messages } = harness();
  await ctx.onSftpDragStart(dragEvent(), file);
  await ctx.cancelSftpDownload();
  assert.equal(calls.length, 1, 'The mouse drag itself is cancelled by the native drag session');
  calls[0].args.onEvent.onmessage({ phase: 'dropped', path: '/local/site.json' });
  assert.equal(ctx.sftpDownloadTask.started, true);
  assert.equal(ctx.sftpDownloadTask.progress, null);
  await ctx.cancelSftpDownload();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].command, 'sftp_cancel_download');
  assert.deepEqual(plain(calls[1].args), { transferId: 'transfer-1' });
  calls[0].args.onEvent.onmessage({ phase: 'cancelled' });
  assert.equal(ctx.sftpNativeDragging, false);
  assert.equal(ctx.sftpXfer, '');
  assert.equal(ctx.sftpDownloadTask, null);
  assert.equal(messages.filter(({ kind }) => kind === 'err').length, 0);
});

test('native drag startup and download failures both clear busy state and report the error', async () => {
  for (const startupFailure of [true, false]) {
    const { ctx, calls, messages } = harness();
    if (startupFailure) ctx.invoke = async () => { throw Error('native drag unavailable'); };
    await ctx.onSftpDragStart(dragEvent(), file);
    if (!startupFailure) calls[0].args.onEvent.onmessage({ phase: 'error', error: 'download connection lost' });
    assert.equal(ctx.sftpNativeDragging, false);
    assert.equal(ctx.sftpXfer, '');
    assert.equal(ctx.sftpDownloadTask, null);
    assert.equal(messages.at(-1).kind, 'err');
    assert.match(messages.at(-1).message, startupFailure ? /native drag unavailable/ : /download connection lost/);
  }
});

test('download history trusts the backend cancellation result instead of the pending cancel button', async () => {
  for (const [failure, status, kind] of [
    ['下载已取消：用户请求', 'cancelled', undefined],
    [Error('connection lost while requesting cancellation'), 'failed', 'err'],
  ]) {
    const download = deferred();
    const { ctx, calls, messages } = harness();
    ctx.invoke = async (command, args) => {
      calls.push({ command, args });
      if (command === 'sftp_download_entry') return download.promise;
    };
    const pending = ctx.sftpDownload(file);
    await until(() => calls.length === 1);
    calls[0].args.onProgress.onmessage({ phase:'downloading', bytes:1, total_bytes:10, files:0, total_files:1 });
    await ctx.cancelSftpDownload();
    assert.equal(ctx.sftpDownloadTask.cancelling, true);
    download.reject(failure);
    await pending;
    assert.equal(ctx.sftpTransfers[0].status, status);
    assert.equal(messages.at(-1).kind, kind);
    assert.equal(ctx.sftpTransfers[0].cancelling, false);
  }
});

test('upload file selection keeps its source server and records confirmed byte totals', async () => {
  const selection = deferred();
  const { ctx, calls, reloads } = harness({ open:() => selection.promise });
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    assert.equal(ctx.sftpTransfers[0].direction, 'upload');
    args.onProgress.onmessage({ phase:'uploading', bytes:2, total_bytes:12, files:0, total_files:1 });
    assert.equal(ctx.sftpTransfers[0].bytes, 2);
    return { path:args.remote, bytes:12, files:1 };
  };
  const pending = ctx.sftpUpload('/config');
  ctx.sftpConnId = 'server-b';
  selection.resolve('/local/config.json');
  await pending;
  assert.equal(calls[0].command, 'sftp_upload_entry');
  assert.equal(calls[0].args.connId, 'server-a');
  assert.equal(calls[0].args.remote, '/config/config.json');
  assert.equal(ctx.sftpTransfers[0].connId, 'server-a');
  assert.equal(ctx.sftpTransfers[0].localPath, '/local/config.json');
  assert.equal(ctx.sftpTransfers[0].status, 'completed');
  assert.equal(ctx.sftpTransfers[0].bytes, 12);
  assert.equal(ctx.sftpTransfers[0].totalBytes, 12);
  assert.equal(ctx.sftpTransfers[0].files, 1);
  assert.equal(ctx.sftpUploadTask, null);
  assert.equal(ctx.sftpXfer, '');
  assert.equal(reloads.length, 0, 'Changing servers must not reload the old directory on the new server');
});

test('streaming uploads can be cancelled and do not start the next file in the batch', async () => {
  const upload = deferred();
  const { ctx, calls, messages } = harness({ open:async () => ['/local/one.txt', '/local/two.txt'] });
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    if (command === 'sftp_upload_entry') return upload.promise;
  };
  const pending = ctx.sftpUpload();
  await until(() => calls.length === 1);
  const id = calls[0].args.transferId;
  await ctx.cancelSftpTransfer(id);
  assert.equal(calls.length, 1);
  calls[0].args.onProgress.onmessage({ phase:'uploading', bytes:1, total_bytes:10, files:0, total_files:1 });
  await ctx.cancelSftpTransfer(id);
  await ctx.cancelSftpTransfer(id);
  assert.equal(calls.length, 2);
  assert.equal(calls[1].command, 'sftp_cancel_download');
  assert.equal(calls[1].args.transferId, id);
  upload.reject('上传已取消：用户请求');
  await pending;
  assert.equal(ctx.sftpTransfers.length, 1);
  assert.equal(ctx.sftpTransfers[0].status, 'cancelled');
  assert.equal(ctx.sftpUploadTask, null);
  assert.equal(ctx.sftpXfer, '');
  assert.equal(messages.at(-1).kind, undefined);
  assert.match(messages.at(-1).message, /^上传已取消/);
});

test('byte fallback uploads report neither fabricated progress nor cancellation before confirmation', async () => {
  const written = deferred();
  const { ctx, calls } = harness();
  ctx.invoke = async (command, args) => { calls.push({ command, args }); return written.promise; };
  const browserFile = { name:'sample.txt', size:3, arrayBuffer:async () => new Uint8Array([65, 66, 67]).buffer };
  const pending = ctx.sftpUploadDropped([browserFile]);
  await until(() => calls.length === 1);
  assert.equal(calls[0].command, 'sftp_write');
  assert.deepEqual(plain(calls[0].args), { connId:'server-a', path:'/config/sample.txt', b64:'QUJD' });
  assert.equal(ctx.sftpTransfers[0].bytes, 0);
  assert.equal(ctx.sftpTransfers[0].totalBytes, 0);
  assert.equal(ctx.sftpTransfers[0].status, 'running');
  assert.equal(ctx.sftpUploadTask.started, false);
  await ctx.cancelSftpTransfer(ctx.sftpTransfers[0].id);
  assert.equal(calls.length, 1);
  written.resolve();
  await pending;
  assert.equal(ctx.sftpTransfers[0].status, 'completed');
  assert.equal(ctx.sftpTransfers[0].bytes, 3);
  assert.equal(ctx.sftpTransfers[0].totalBytes, 3);
  assert.equal(ctx.sftpTransfers[0].files, 1);
});

test('oversized byte fallback files become failed history without attempting a remote write', async () => {
  const { ctx, calls, messages } = harness();
  await ctx.sftpUploadDropped([{ name:'large.zip', size:26 * 1024 * 1024, arrayBuffer:async () => { throw Error('must not read'); } }]);
  assert.equal(calls.length, 0);
  assert.equal(ctx.sftpTransfers[0].status, 'failed');
  assert.match(ctx.sftpTransfers[0].error, /25MB/);
  assert.equal(messages.at(-1).kind, 'err');
  assert.equal(ctx.sftpUploadTask, null);
  assert.equal(ctx.sftpXfer, '');
});

test('partly successful picker and dropped upload batches retain the failure toast and both records', async () => {
  for (const dropped of [false, true]) {
    const { ctx, calls, messages } = harness({ open:async () => ['/local/one.txt', '/local/two.txt'] });
    ctx.invoke = async (command, args) => {
      calls.push({ command, args });
      if (calls.length === 2) throw Error('second file permission denied');
      return { bytes:12, files:1 };
    };
    if (dropped) await ctx.sftpUploadDropped([
      { name:'one.txt', path:'/local/one.txt' }, { name:'two.txt', path:'/local/two.txt' },
    ]);
    else await ctx.sftpUpload();
    assert.deepEqual(ctx.sftpTransfers.map(record => record.status), ['failed', 'completed']);
    assert.equal(messages.at(-1).kind, 'err');
    assert.match(messages.at(-1).message, /second file permission denied/);
    assert.equal(messages.some(({ message }) => message.startsWith('已上传') || message.startsWith('已拖入上传')), false);
  }
});

test('first dropped upload failure or cancellation refreshes partial files only on the original connection', async () => {
  for (const [failure, status] of [[Error('connection lost after a partial write'), 'failed'], ['上传已取消：用户请求', 'cancelled']]) {
    for (const switched of [false, true]) {
      const { ctx, calls, reloads } = harness();
      ctx.invoke = async (command, args) => {
        calls.push({ command, args });
        if (switched) ctx.sftpConnId = 'server-b';
        throw failure;
      };
      await ctx.sftpUploadDropped([{ name:'partial.bin', path:'/local/partial.bin' }], '/config');
      assert.equal(calls.length, 1);
      assert.equal(calls[0].args.connId, 'server-a');
      assert.equal(calls[0].args.remote, '/config/partial.bin');
      assert.equal(ctx.sftpTransfers[0].status, status);
      assert.deepEqual(reloads, switched ? [] : [{ path:'/config', force:true }]);
      assert.equal(ctx.sftpXfer, '');
    }
  }
});

test('per-window persistence loads only its own history and clear/reveal keep active work intact', async () => {
  const { ctx, storage, reveals } = harness();
  ctx.addSftpTransfer('active', 'pending.txt', 'upload', 'server-a', '/config/pending.txt');
  ctx.addSftpTransfer('done', 'download.txt', 'download', 'server-a', '/config/download.txt');
  ctx.finishSftpTransfer('done', 'completed', { localPath:'/local/download.txt' });
  const ownHistory = storage.get(ctx.sftpHistoryKey);
  const otherKey = `${transferHistory.SFTP_TRANSFER_HISTORY_KEY}:another-window`;
  storage.set(otherKey, '[]');
  assert.equal(ctx.loadSftpTransfers().length, 2);
  assert.equal(ctx.loadSftpTransfers().find(record => record.id === 'active').status, 'interrupted');
  await ctx.revealSftpTransfer(ctx.sftpTransfers.find(record => record.id === 'active'));
  await ctx.revealSftpTransfer(ctx.sftpTransfers.find(record => record.id === 'done'));
  assert.deepEqual(reveals, ['/local/download.txt']);
  ctx.clearSftpTransfers();
  assert.deepEqual(ctx.sftpTransfers.map(record => record.id), ['active']);
  assert.notEqual(storage.get(ctx.sftpHistoryKey), ownHistory);
  assert.equal(storage.get(otherKey), '[]');
});

test('local drag metadata preserves file order and reads directory entries before awaiting IPC', () => {
  const { ctx } = harness();
  const files = [
    { name:' first.zip ', size:80 * 1024 * 1024, lastModified:1000 },
    { name:'folder', size:0, lastModified:2000 },
    { name:'unknown.bin', size:4, lastModified:3000 },
  ];
  const data = { items:[
    { kind:'string', webkitGetAsEntry:() => { throw Error('must not inspect text'); } },
    { kind:'file', webkitGetAsEntry:() => ({ isDirectory:false }) },
    { kind:'file', webkitGetAsEntry:() => ({ isDirectory:true }) },
    { kind:'file', webkitGetAsEntry:() => { throw Error('browser did not expose an entry'); } },
  ] };
  const entries = ctx.localDropEntries(data, files);
  data.items = [];
  assert.deepEqual(plain(entries), [
    { name:' first.zip ', size:80 * 1024 * 1024, mtime:1000, directory:false },
    { name:'folder', size:0, mtime:2000, directory:true },
    { name:'unknown.bin', size:4, mtime:3000, directory:null },
  ]);
});

test('verified large files and directories stream once in native order without reading browser bytes', async () => {
  const { ctx, calls, dialogs } = harness();
  let byteReads = 0;
  const files = [
    { name:' large backup.zip ', size:80 * 1024 * 1024, lastModified:1000, path:'/forged/path.zip' },
    { name:'folder', size:0, lastModified:2000, path:'/forged/folder' },
  ].map(file => ({ ...file, arrayBuffer:async () => { byteReads++; throw Error('must not read browser bytes'); } }));
  const event = dropEvent(files, [false, true]);
  const entries = ctx.localDropEntries(event.dataTransfer, files);
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    if (command === 'sftp_consume_local_drop') return { paths:['/native/ large backup.zip ', '/native/folder'] };
    return { bytes:100, files:1 };
  };
  await ctx.sftpUploadDropped(files, '/config/assets', 'server-a', { capture:Promise.resolve({ token:'verified-drop', count:2 }), entries });
  assert.equal(byteReads, 0);
  assert.equal(dialogs.length, 0);
  assert.deepEqual(calls.map(call => call.command), ['sftp_consume_local_drop', 'sftp_upload_entry', 'sftp_upload_entry']);
  assert.deepEqual(plain(calls[0].args), { token:'verified-drop', entries:plain(entries) });
  assert.deepEqual(calls.slice(1).map(call => [call.args.local, call.args.remote]), [
    ['/native/ large backup.zip ', '/config/assets/ large backup.zip '],
    ['/native/folder', '/config/assets/folder'],
  ]);
  assert.equal(ctx.sftpTransfers.every(record => record.status === 'completed'), true);
});

test('native consume errors never fall back to browser bytes or an unverified File.path', async () => {
  for (const failure of ['token expired', '本地项目数量不匹配', '本地项目顺序不匹配']) {
    const { ctx, calls, messages } = harness();
    let byteReads = 0;
    const files = [{ name:'small.txt', size:3, lastModified:1000, path:'/unverified/small.txt', arrayBuffer:async () => { byteReads++; return new ArrayBuffer(3); } }];
    ctx.invoke = async (command, args) => { calls.push({ command, args }); throw Error(failure); };
    await ctx.sftpUploadDropped(files, '/config', 'server-a', {
      capture:Promise.resolve({ token:'stale-token', count:1 }),
      entries:[{ name:'small.txt', size:3, mtime:1000, directory:false }],
    });
    assert.deepEqual(calls.map(call => call.command), ['sftp_consume_local_drop']);
    assert.equal(byteReads, 0);
    assert.equal(ctx.sftpTransfers.length, 0);
    assert.equal(ctx.sftpXfer, '');
    assert.equal(messages.at(-1).kind, 'err');
    assert.ok(messages.at(-1).message.includes(failure));
  }
});

test('count, descriptor count, returned path count and returned order mismatches reject the whole drop', async () => {
  for (const mismatch of ['capture-count', 'entry-count', 'path-count', 'path-order']) {
    const { ctx, calls, messages } = harness();
    const files = ['first.txt', 'second.txt'].map(name => ({ name, size:4, lastModified:1000, path:`/unverified/${name}`, arrayBuffer:async () => { throw Error('must not fallback'); } }));
    const entries = files.map(file => ({ name:file.name, size:file.size, mtime:file.lastModified, directory:false }));
    ctx.invoke = async (command, args) => {
      calls.push({ command, args });
      if (command === 'sftp_discard_local_drop') return;
      return { paths:mismatch === 'path-count' ? ['/native/first.txt'] : ['/native/second.txt', '/native/first.txt'] };
    };
    await ctx.sftpUploadDropped(files, '/config', 'server-a', {
      capture:Promise.resolve({ token:'mismatch-token', count:mismatch === 'capture-count' ? 1 : 2 }),
      entries:mismatch === 'entry-count' ? entries.slice(0, 1) : entries,
    });
    assert.deepEqual(calls.map(call => call.command), [mismatch.endsWith('count') && mismatch !== 'path-count' ? 'sftp_discard_local_drop' : 'sftp_consume_local_drop']);
    assert.equal(ctx.sftpTransfers.length, 0);
    assert.equal(ctx.sftpXfer, '');
    assert.match(messages.at(-1).message, /不匹配/);
  }
});

test('quick drop snapshots the connection and hovered child directory before capture finishes', async () => {
  const captured = deferred();
  const { ctx, calls, reloads } = harness();
  const files = [{ name:'sample.txt', size:60 * 1024 * 1024, lastModified:1000, arrayBuffer:async () => { throw Error('must not read'); } }];
  const event = dropEvent(files, [false], folder.path);
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    if (command === 'sftp_capture_local_drop') return captured.promise;
    if (command === 'sftp_consume_local_drop') return { paths:['/native/sample.txt'] };
    return { bytes:files[0].size, files:1 };
  };
  ctx.onSftpDrop(event);
  assert.equal(event.prevented, true);
  assert.equal(event.stopped, true);
  assert.equal(ctx.sftpDropCapture, null, 'An accepted drop takes ownership of its token');
  assert.equal(calls[0].command, 'sftp_capture_local_drop');
  ctx.sftpConnId = 'server-b';
  ctx.sftpPath = '/other-server';
  ctx.sftpRows = [];
  event.dataTransfer.items = [];
  ctx.discardSftpLocalDrop();
  captured.resolve({ token:'late-capture', count:1 });
  await until(() => ctx.sftpXfer === '' && calls.some(call => call.command === 'sftp_upload_entry'));
  const upload = calls.find(call => call.command === 'sftp_upload_entry');
  assert.equal(upload.args.connId, 'server-a');
  assert.equal(upload.args.remote, `${folder.path}/sample.txt`);
  assert.equal(ctx.sftpTransfers[0].connId, 'server-a');
  assert.equal(reloads.length, 0);
  assert.equal(calls.some(call => call.command === 'sftp_discard_local_drop'), false);
  assert.equal(calls.find(call => call.command === 'sftp_consume_local_drop').args.entries[0].directory, false);
});

test('hover captures once, internal movement retains it, and leaving discards a late token', async () => {
  const captured = deferred();
  const { ctx, calls } = harness();
  const event = dropEvent([{ name:'sample.txt', size:3, lastModified:1000 }], [false], folder.path);
  ctx.invoke = async (command, args) => {
    calls.push({ command, args });
    if (command === 'sftp_capture_local_drop') return captured.promise;
  };
  ctx.onSftpDragOver(event);
  ctx.onSftpDragOver(event);
  assert.equal(calls.length, 1);
  assert.equal(ctx.sftpFileOver, true);
  assert.deepEqual(plain(ctx.sftpDropTarget), { connId:'server-a', remoteDir:folder.path });
  event.relatedTarget = new MockElement(event.currentTarget);
  ctx.onSftpDragLeave(event);
  assert.ok(ctx.sftpDropCapture);
  event.relatedTarget = null;
  ctx.onSftpDragLeave(event);
  assert.equal(ctx.sftpDropCapture, null);
  assert.equal(ctx.sftpFileOver, false);
  assert.equal(ctx.sftpDropTarget, null);
  captured.resolve({ token:'abandoned-token', count:1 });
  await until(() => calls.length === 2);
  assert.equal(calls[1].command, 'sftp_discard_local_drop');
  assert.deepEqual(plain(calls[1].args), { token:'abandoned-token' });
});

test('non-mac browser-only drops reject folders while small files use only byte fallback', async () => {
  for (const directory of [true, false]) {
    const { ctx, calls, messages } = harness({ isMac:false });
    let byteReads = 0;
    const files = [{ name:directory ? 'folder' : 'sample.txt', size:3, lastModified:1000, path:'/unverified/path', arrayBuffer:async () => { byteReads++; return new Uint8Array([1, 2, 3]).buffer; } }];
    ctx.invoke = async (command, args) => { calls.push({ command, args }); };
    ctx.beginSftpLocalDrop();
    assert.equal(calls.length, 0);
    assert.equal(ctx.sftpDropCapture, null);
    await ctx.sftpUploadDropped(files, '/config', 'server-a', {
      capture:Promise.resolve(null), entries:[{ name:files[0].name, size:3, mtime:1000, directory }],
    });
    assert.equal(byteReads, directory ? 0 : 1);
    assert.deepEqual(calls.map(call => call.command), directory ? [] : ['sftp_write']);
    assert.equal(ctx.sftpTransfers.length, directory ? 0 : 1);
    if (directory) assert.match(messages.at(-1).message, /无法读取/);
  }
});

test('mac native capture rejection never falls back to browser bytes or an unverified local path', async () => {
  const captured = deferred();
  const { ctx, calls, messages } = harness();
  let byteReads = 0;
  const files = [{ name:'small.txt', size:3, lastModified:1000, path:'/unverified/small.txt', arrayBuffer:async () => { byteReads++; return new ArrayBuffer(3); } }];
  const event = dropEvent(files);
  ctx.invoke = async (command, args) => { calls.push({ command, args }); return captured.promise; };
  ctx.onSftpDragOver(event);
  ctx.onSftpDrop(event);
  assert.ok(ctx.sftpXfer);
  captured.reject(Error('native drag sequence expired'));
  await until(() => ctx.sftpXfer === '');
  assert.deepEqual(calls.map(call => call.command), ['sftp_capture_local_drop']);
  assert.equal(byteReads, 0);
  assert.equal(ctx.sftpTransfers.length, 0);
  assert.equal(messages.at(-1).kind, 'err');
  assert.match(messages.at(-1).message, /native drag sequence expired/);
});

test('busy targets, internal conversation drags and text drops do not start a local upload', async () => {
  for (const blocked of [
    { sftpBusy:true }, { sftpXfer:'other transfer' }, { sftpNativeDragging:true },
    { sftpConnId:'' }, { sftpPath:'' },
  ]) {
    const { ctx, calls } = harness(blocked);
    const event = dropEvent([{ name:'sample.txt', size:3, lastModified:1000 }]);
    ctx.onSftpDragOver(event);
    assert.equal(event.dataTransfer.dropEffect, 'none');
    assert.equal(ctx.sftpFileOver, false);
    ctx.onSftpDrop(event);
    assert.equal(calls.length, 0);
    assert.equal(ctx.sftpTransfers.length, 0);
  }
  for (const types of [['Files', 'application/x-pilot-conversation'], ['text/plain']]) {
    const { ctx, calls } = harness();
    const event = dropEvent([{ name:'sample.txt', size:3, lastModified:1000 }]);
    event.dataTransfer.types = types;
    ctx.onSftpDragOver(event);
    ctx.onSftpDrop(event);
    assert.equal(event.prevented, false);
    assert.equal(event.stopped, false);
    assert.equal(calls.length, 0);
  }
});

test('composer attachment drops keep their existing route and never invoke the SFTP bridge', async () => {
  const { ctx, calls, attached } = harness();
  const files = [{ name:'message-attachment.txt', size:3 }, { name:'picture.png', size:5 }];
  const event = dropEvent(files);
  ctx.onComposerDragOver(event);
  assert.equal(ctx.composerFileOver, true);
  assert.equal(ctx.sftpFileOver, false);
  ctx.onComposerDrop(event);
  assert.deepEqual(attached, files);
  assert.equal(ctx.composerFileOver, false);
  assert.equal(ctx.sftpTransfers.length, 0);
  assert.equal(ctx.sftpDropCapture, null);
  assert.equal(calls.length, 0);
});

test('scoped composer drops and clipboard attachments keep their conversation upload route', async () => {
  const { ctx, calls, attached, paneAttachments } = harness();
  const files = [{ name:'conversation-file.txt', size:3 }];
  await ctx.uploadPaneFiles('chat-b', files);
  assert.deepEqual(paneAttachments, [{ id:'chat-b', file:files[0] }]);
  const event = { ...dragEvent(), clipboardData:{ files, items:[] } };
  ctx.onComposerPaste(event, 'chat-b');
  assert.equal(event.prevented, true);
  assert.deepEqual(paneAttachments, [{ id:'chat-b', file:files[0] }, { id:'chat-b', file:files[0] }]);
  assert.equal(attached.length, 0);
  assert.equal(ctx.sftpTransfers.length, 0);
  assert.equal(calls.length, 0);
});
