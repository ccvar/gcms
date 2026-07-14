<script lang="ts">
  // 用量圆环（对标 Claude Code 的输入框指示器）：~16px 圆弧＝当前会话上下文占用比例
  //（无会话时只有浅色底环），点开小卡片看「上下文窗口 + 本地用量」。
  // 本地用量取数从 ModelFx 面板整段搬家而来（usage_stats，近 5 小时 / 今日零点两个窗口）。
  import { invoke } from '@tauri-apps/api/core';
  import { tip } from './tip';

  let {
    /** 当前会话上下文 token（0＝无会话/无读数，圆弧为 0） */
    ctx = 0,
    /** 假定上下文上限（调用方传 ctxLimitAdaptive(brain, model, ctx)：按模型细分＋实测自适应升档；0＝不显示上下文行） */
    limit = 0,
    /** 本会话累计 token（total_tokens 口径；0＝无会话/启动器，不显示累计行） */
    total = 0,
  }: { ctx?: number; limit?: number; total?: number } = $props();

  const pct = $derived(ctx > 0 && limit > 0 ? Math.min(100, Math.round((ctx / limit) * 100)) : 0);
  const warn = $derived(pct >= 80);

  let open = $state(false);
  let root = $state<HTMLElement>();
  let cardStyle = $state('');

  // hover 气泡走共享 $lib/tip action（原生 title 贴窗口边会被系统裁切）；
  // 弹卡打开时传空文案＝action 即刻隐藏且不再触发。
  const tipText = $derived(
    pct > 0
      ? `上下文 ${pct}% · ${fmtTok(ctx)} / ${fmtTok(limit)}${total > 0 ? ` · 累计 ${fmtTok(total)}` : ''}`
      : total > 0 ? `累计 ${fmtTok(total)} tokens` : '本地用量',
  );

  // fixed 弹层定位（对齐 ModelFx.position() 的模式）：哪边空间大开哪边，左缘钳制防溢出。
  function position() {
    const btn = root?.querySelector('.ur-btn') as HTMLElement | null;
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    const width = 300;
    const left = Math.max(12, Math.min(r.left - width + 24, window.innerWidth - width - 12));
    const margin = 14;
    const spaceAbove = r.top - margin;
    const spaceBelow = window.innerHeight - r.bottom - margin;
    if (spaceBelow > spaceAbove) {
      cardStyle = `top:${Math.round(r.bottom + 6)}px; left:${Math.round(left)}px; width:${width}px; max-height:${Math.round(spaceBelow - 6)}px;`;
    } else {
      cardStyle = `bottom:${Math.round(window.innerHeight - r.top + 6)}px; left:${Math.round(left)}px; width:${width}px; max-height:${Math.round(spaceAbove - 6)}px;`;
    }
  }

  // 本地用量参考（近 5 小时 / 今日，按 brain 汇总）：打开卡片时拉一次。
  // 官方订阅额度无查询接口，这只是本地体感计量，换算不了剩余百分比。
  type UsageStats = { window_a: Record<string, number>; window_b: Record<string, number> };
  let usage = $state<UsageStats | null>(null);
  async function loadUsage() {
    const midnight = new Date();
    midnight.setHours(0, 0, 0, 0);
    try {
      usage = await invoke<UsageStats>('usage_stats', {
        sinceA: Math.floor(Date.now() / 1000) - 5 * 3600,
        sinceB: Math.floor(midnight.getTime() / 1000),
      });
    } catch { usage = null; }
  }
  function fmtTok(n: number | undefined): string {
    const v = n ?? 0;
    if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M';
    if (v >= 1e3) return Math.round(v / 1e3) + 'k';
    return String(v);
  }
  function toggle() { open = !open; if (open) { requestAnimationFrame(position); void loadUsage(); } }

  function onDoc(e: MouseEvent) { if (root && !root.contains(e.target as Node)) open = false; }
  function onKey(e: KeyboardEvent) { if (e.key === 'Escape') open = false; }
  function onScroll(e: Event) {
    const card = root?.querySelector('.ur-card');
    if (card && (e.target === card || (card as HTMLElement).contains(e.target as Node))) return;
    open = false;
  }
  $effect(() => {
    if (!open) return;
    document.addEventListener('mousedown', onDoc, true);
    document.addEventListener('keydown', onKey);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', position);
    return () => {
      document.removeEventListener('mousedown', onDoc, true);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', position);
    };
  });

  // 16px 圆环：r=6.5 → 周长 ≈ 40.84，用 dasharray 画占用弧（-90° 起点＝12 点方向）。
  const CIRC = 2 * Math.PI * 6.5;
</script>

<div class="ur" bind:this={root}>
  <button type="button" class="ur-btn" class:open onclick={toggle} use:tip={open ? '' : tipText}>
    <svg width="13" height="13" viewBox="0 0 16 16">
      <circle cx="8" cy="8" r="6.5" fill="none" stroke="var(--border2, #e1dfd8)" stroke-width="2" />
      {#if pct > 0}
        <circle class="ur-arc" class:warn cx="8" cy="8" r="6.5" fill="none" stroke-width="2" stroke-linecap="round"
          stroke-dasharray="{Math.max(1.2, (pct / 100) * CIRC)} {CIRC}" transform="rotate(-90 8 8)" />
      {/if}
    </svg>
  </button>
  {#if open}
    <div class="ur-card" style={cardStyle} data-no-drag>
      {#if pct > 0}
        <div class="ur-sec">上下文窗口<span class="ur-sub">{fmtTok(ctx)} / {fmtTok(limit)}（{pct}%）</span></div>
        <div class="ur-bar"><span class="ur-fill" class:warn style="width:{Math.max(3, pct)}%"></span></div>
      {/if}
      {#if total > 0}<div class="ur-total">本会话累计 {fmtTok(total)} tokens</div>{/if}
      {#if pct > 0 || total > 0}<div class="ur-div"></div>{/if}
      <div class="ur-sec">本地用量<span class="ur-sub">token · 仅供参考</span></div>
      <div class="ur-row"><span>近 5 小时</span><b>Claude {fmtTok(usage?.window_a?.claude)} · Codex {fmtTok(usage?.window_a?.codex)} · Grok {fmtTok(usage?.window_a?.grok)}</b></div>
      <div class="ur-row"><span>今日</span><b>Claude {fmtTok(usage?.window_b?.claude)} · Codex {fmtTok(usage?.window_b?.codex)} · Grok {fmtTok(usage?.window_b?.grok)}</b></div>
    </div>
  {/if}
</div>

<style>
  .ur { position: relative; display: inline-flex; }
  /* 环 13px（viewBox 16 等比缩放，线宽随之保持比例）；按钮吃 --chip-h 定高，与底栏其他 chip 同基线。 */
  .ur-btn { display: inline-flex; align-items: center; justify-content: center; width: 24px; height: var(--chip-h, 24px); box-sizing: border-box; padding: 0; border: none; border-radius: 6px; background: transparent; cursor: pointer; }
  .ur-btn:hover, .ur-btn.open { background: #f1efe9; }
  .ur-arc { stroke: var(--accent, #33302a); }
  .ur-arc.warn { stroke: var(--warn, #b45309); }

  .ur-card {
    position: fixed; z-index: 95; display: flex; flex-direction: column; overflow-y: auto;
    background: #fff; border: 1px solid var(--border2, #e2dfd7); border-radius: 12px;
    box-shadow: 0 12px 32px rgba(30, 25, 15, .16); padding: 7px 8px 8px;
  }
  .ur-sec { flex: none; display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 10.5px; font-weight: 600; letter-spacing: .04em; color: var(--faint, #9b968c); padding: 2px 2px 4px; }
  .ur-sub { font-weight: 500; letter-spacing: 0; color: var(--dim, #6b675f); white-space: nowrap; }
  .ur-bar { flex: none; height: 5px; margin: 0 2px 2px; border-radius: 999px; background: #eceae3; overflow: hidden; }
  .ur-fill { display: block; height: 100%; border-radius: 999px; background: var(--accent, #33302a); }
  .ur-fill.warn { background: var(--warn, #b45309); }
  .ur-total { flex: none; font-size: 10.5px; color: var(--faint, #9b968c); padding: 3px 2px 0; }
  .ur-div { flex: none; height: 1px; margin: 6px 0; background: var(--border, #ecebe6); }
  .ur-row { flex: none; display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 11.5px; color: var(--dim, #6b675f); padding: 1.5px 2px; }
  .ur-row b { font-weight: 500; color: var(--text, #26241f); white-space: nowrap; }
</style>
