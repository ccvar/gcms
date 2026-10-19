<script lang="ts">
  import { tick } from 'svelte';
  import { isTransferActive, transferPercent, transferMetrics, formatTransferBytes, formatTransferDuration, transferStatusText, transferProgressText, type TransferRecord } from './sftpTransfers';

  let { records, busy = false, canDownload = false, selectedName = '', cancellableIds = [], onupload, ondownload, oncancel, onclear, onreveal }: {
    records: TransferRecord[]; busy?: boolean; canDownload?: boolean; selectedName?: string; cancellableIds?: string[];
    onupload: () => void; ondownload: () => void; oncancel: (id: string) => void;
    onclear: () => void; onreveal: (record: TransferRecord) => void;
  } = $props();
  let opened = $state(false);
  let trigger = $state<HTMLButtonElement>();
  let panel = $state<HTMLDivElement>();
  let position = $state({ left: 0, top: 0, width: 360, height: 480 });
  const active = $derived(records.filter(isTransferActive));
  const settled = $derived(records.filter(r => !isTransferActive(r)));
  const ordered = $derived([...active, ...settled]);
  // The file panel uses a size container/transform. Keep viewport coordinates
  // valid by rendering this fixed popover outside that containing block.
  function portal(node: HTMLDivElement) {
    document.body.appendChild(node);
    return { destroy() { node.remove(); } };
  }
  function place() {
    if (!opened || !trigger) return;
    const anchor = trigger.getBoundingClientRect();
    const width = Math.min(370, window.innerWidth - 16);
    const roomBelow = window.innerHeight - anchor.bottom - 16;
    const below = roomBelow >= 240 || anchor.top < roomBelow;
    const height = Math.max(160, Math.min(480, below ? roomBelow : anchor.top - 16));
    position = { width, height, left: Math.max(8, Math.min(window.innerWidth - width - 8, anchor.right - width)), top: below ? anchor.bottom + 6 : Math.max(8, anchor.top - height - 6) };
  }
  async function toggle() {
    opened = !opened;
    if (opened) { place(); await tick(); panel?.focus(); }
  }
  function dismiss(focus = false) { opened = false; if (focus) trigger?.focus(); }
  function outside(event: PointerEvent) {
    if (opened && event.target instanceof Node && !panel?.contains(event.target) && !trigger?.contains(event.target)) dismiss();
  }
  function keydown(event: KeyboardEvent) {
    if (opened && event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); dismiss(true); }
  }
  function when(at: number) { return new Date(at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }); }
</script>

<svelte:window onpointerdown={outside} onkeydown={keydown} onresize={place} />

<button bind:this={trigger} type="button" class="transfer-trigger" class:open={opened} class:active={active.length > 0}
  aria-label="传输" title="传输" aria-haspopup="dialog" aria-expanded={opened} onclick={toggle}>
  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M5 12.5v-9m-2.5 2.6L5 3.5l2.5 2.6M11 3.5v9m-2.5-2.6 2.5 2.6 2.5-2.6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
  {#if active.length}<span class="activity" aria-hidden="true"></span>{/if}
</button>

{#if opened}
  <div bind:this={panel} use:portal class="transfer-panel" role="dialog" aria-label="传输记录" tabindex="-1"
    style="left:{position.left}px;top:{position.top}px;width:{position.width}px;max-height:{position.height}px">
    <header><strong>传输</strong><span class="summary">{active.length ? `${active.length} 项进行中` : '传输记录'}</span><button class="text-button" disabled={!settled.length} onclick={onclear} title="仅清除记录，不删除文件">清除记录</button><button class="icon-button" aria-label="关闭传输记录" onclick={() => dismiss(true)}>×</button></header>
    <div class="transfer-list" aria-live="polite" aria-relevant="additions">
      {#if !ordered.length}
        <div class="empty"><b>暂无传输记录</b><span>拖到 Finder 下载，拖入文件列表上传</span></div>
      {:else}
        {#each ordered as record (record.id)}
          {@const running = isTransferActive(record)}
          {@const percent = transferPercent(record)}
          {@const metrics = transferMetrics(record)}
          <div class="transfer-row" class:failed={record.status === 'failed'}>
            <svg class="direction" width="16" height="16" viewBox="0 0 16 16" fill="none" aria-label={record.direction === 'upload' ? '上传' : '下载'} role="img"><path d={record.direction === 'upload' ? 'M8 11V3m-3 3 3-3 3 3M3 13h10' : 'M8 2v8M5 7l3 3 3-3M3 13h10'} stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <div class="transfer-body">
              <div class="title"><b title={record.name}>{record.name}</b><span class="status">{transferStatusText(record)}</span></div>
              <div class="subtitle" title={`${record.connectionName} · ${record.remotePath}`}><span>{record.connectionName || '远程连接'}</span><time>{when(record.finishedAt || record.startedAt)}</time></div>
              {#if running}
                {#if percent === null}<progress max="100" aria-label={`${record.name} 传输进度`}></progress>{:else}<progress max="100" value={percent} aria-label={`${record.name} 传输进度`}></progress>{/if}
                <div class="detail" title={transferProgressText(record)}>{transferProgressText(record)}</div>
                {#if metrics.bytesPerSecond !== null}
                  <div class="detail rate">{`平均 ${formatTransferBytes(metrics.bytesPerSecond)}/s${metrics.etaSeconds !== null ? ` · 剩余约 ${formatTransferDuration(metrics.etaSeconds)}` : ''}`}</div>
                {/if}
              {:else if record.error}
                <div class="detail error" title={record.error}>{record.error}</div>
              {:else if record.localPath}
                <div class="detail" title={record.localPath}>{record.localPath}</div>
              {/if}
            </div>
            <div class="row-action">
              {#if running}
                <button class="icon-button" aria-label={`取消传输 ${record.name}`} title={record.cancelling ? '正在取消' : '取消传输'} disabled={!cancellableIds.includes(record.id) || record.cancelling} onclick={() => oncancel(record.id)}>{#if record.cancelling}<span class="spinner"></span>{:else}×{/if}</button>
              {:else if record.status === 'completed' && record.direction === 'download' && record.localPath}
                <button class="icon-button" aria-label={`在文件夹中显示 ${record.name}`} title="在文件夹中显示" onclick={() => onreveal(record)}><svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="m11.2 11.2 3 3M12 7A5 5 0 1 1 2 7a5 5 0 0 1 10 0" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" /></svg></button>
              {:else if record.status === 'completed'}<span class="complete" title="已完成">✓</span>{/if}
            </div>
          </div>
        {/each}
      {/if}
    </div>
    <footer><button class="text-button" disabled={busy} onclick={onupload}>上传文件…</button><button class="text-button" disabled={busy || !canDownload} title={selectedName ? `下载 ${selectedName}` : '先在列表中选择文件或文件夹'} onclick={ondownload}>下载所选…</button></footer>
  </div>
{/if}

<style>
  .transfer-trigger { position:relative; display:inline-flex; align-items:center; justify-content:center; flex:none; width:24px; height:24px; padding:0; border:1px solid var(--border); border-radius:6px; background:var(--bg); color:var(--dim); cursor:pointer; }
  .transfer-trigger:hover, .transfer-trigger.open { color:var(--text); border-color:var(--border2, var(--border)); background:var(--rail); }
  .activity { position:absolute; right:1px; top:1px; width:5px; height:5px; border-radius:50%; background:var(--accent, #34322c); box-shadow:0 0 0 2px var(--bg); }
  .transfer-panel { position:fixed; z-index:1100; display:flex; flex-direction:column; overflow:hidden; box-sizing:border-box; border:1px solid var(--border); border-radius:10px; background:var(--bg); color:var(--text); box-shadow:0 10px 32px #0002; font-size:12px; outline:none; }
  header { display:flex; align-items:center; gap:8px; padding:8px 10px; border-bottom:1px solid var(--border); flex:none; }
  header strong { font-size:13px; font-weight:600; }
  .summary { flex:1; color:var(--dim); font-size:11px; white-space:nowrap; }
  button { font:inherit; }
  .text-button, .icon-button { display:inline-flex; align-items:center; justify-content:center; height:24px; border:0; border-radius:5px; color:var(--dim); background:transparent; cursor:pointer; flex:none; }
  .text-button { padding:0 6px; font-size:11px; }
  .icon-button { width:24px; padding:0; font-size:17px; line-height:1; }
  button:hover:not(:disabled) { color:var(--text); background:var(--rail); }
  button:focus-visible { outline:2px solid var(--accent); outline-offset:1px; }
  button:disabled { opacity:.4; cursor:default; }
  .transfer-list { overflow-y:auto; min-height:0; overscroll-behavior:contain; }
  .transfer-row { display:flex; align-items:flex-start; gap:8px; padding:10px 12px; border-bottom:1px solid var(--border); }
  .transfer-row:last-child { border-bottom:0; }
  .direction { margin-top:2px; flex:none; color:var(--dim); }
  .transfer-body { flex:1; min-width:0; }
  .title { display:flex; align-items:center; gap:6px; line-height:18px; }
  .title b { flex:1; min-width:0; overflow:hidden; white-space:nowrap; text-overflow:ellipsis; font-weight:500; }
  .status { color:var(--dim); white-space:nowrap; font-size:11px; }
  .failed .status, .error { color:var(--err, #c54a43); }
  .subtitle { display:flex; align-items:center; gap:8px; color:var(--dim); font-size:10px; line-height:16px; }
  .subtitle span { flex:1; overflow:hidden; white-space:nowrap; text-overflow:ellipsis; }
  time { flex:none; font-variant-numeric:tabular-nums; }
  .detail { overflow:hidden; white-space:nowrap; text-overflow:ellipsis; font-size:10px; line-height:16px; color:var(--dim); font-variant-numeric:tabular-nums; }
  .detail.error { color:var(--err, #c54a43); }
  .row-action { width:24px; min-height:24px; flex:none; }
  .complete { display:flex; width:24px; height:24px; align-items:center; justify-content:center; color:var(--dim); }
  progress { appearance:none; display:block; width:100%; height:2px; margin:6px 0 4px; border:0; border-radius:2px; overflow:hidden; background:var(--border); accent-color:var(--dim); }
  progress::-webkit-progress-bar { background:var(--border); }
  progress::-webkit-progress-value { background:var(--dim); }
  progress::-moz-progress-bar { background:var(--dim); }
  progress:indeterminate { background:linear-gradient(90deg,var(--border) 25%,var(--dim) 50%,var(--border) 75%); background-size:200% 100%; animation:progress 1.5s linear infinite; }
  progress:indeterminate::-webkit-progress-bar { background:transparent; }
  .empty { padding:30px 12px; text-align:center; color:var(--dim); }
  .empty b { display:block; margin-bottom:6px; font-weight:500; }
  .empty span { font-size:11px; }
  footer { display:flex; align-items:center; gap:6px; padding:6px 10px; border-top:1px solid var(--border); flex:none; }
  .spinner { width:10px; height:10px; border:1.4px solid currentColor; border-right-color:transparent; border-radius:50%; animation:spin .7s linear infinite; }
  @keyframes progress { to { background-position:-200% 0; } }
  @keyframes spin { to { transform:rotate(360deg); } }
  @media(prefers-reduced-motion:reduce) { progress:indeterminate,.spinner { animation:none; } }
</style>
