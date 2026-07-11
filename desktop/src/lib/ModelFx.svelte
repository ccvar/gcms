<script lang="ts">
  // 模型 + 推理强度 合并面板：点开一个浮层，上半是模型列表、下半是可拖动的强度滑杆
  //（对标 ChatGPT/Claude 客户端的交互）。选择即回调，父组件负责真正落库/持久化。
  import { invoke } from '@tauri-apps/api/core';
  import BrainIcon from './BrainIcon.svelte';

  type Opt = { value: string; label: string; sub?: string; icon?: string; disabled?: boolean };
  let {
    options = [] as Opt[],
    value = '',
    effort = '',
    /** 运行中锁模型（换模型影响本轮语义），强度仍可调（下轮生效） */
    lockModel = false,
    onpick = (_v: string) => {},
    oneffort = (_v: string) => {},
  } = $props();

  const STOPS = [
    { v: '', l: '默认', d: '跟随模型' },
    { v: 'low', l: '低', d: '更快更省' },
    { v: 'medium', l: '中', d: '均衡' },
    { v: 'high', l: '高', d: '更缜密 · 更慢' },
  ];

  let open = $state(false);
  let root = $state<HTMLElement>();
  let track = $state<HTMLElement>();
  let dragging = $state(false);
  let menuStyle = $state('');

  const current = $derived(options.find((o) => o.value === value));
  const idx = $derived(Math.max(0, STOPS.findIndex((s) => s.v === effort)));
  /** 拖动中的原始位置（0-100）；null＝未在拖，显示已选档位 */
  let dragPct = $state<number | null>(null);
  const pct = $derived(dragPct ?? (idx / (STOPS.length - 1)) * 100);
  /** 面板标题实时预览：拖到哪就显示哪个档（松手才真正提交） */
  const liveIdx = $derived(dragPct === null ? idx : Math.round((dragPct / 100) * (STOPS.length - 1)));

  // 浮层 fixed 定位（触发器都在底部 composer：向上开），左边界钳制防溢出。
  function position() {
    const btn = root?.querySelector('.fx-trigger') as HTMLElement | null;
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    const width = 320;
    const left = Math.max(12, Math.min(r.left, window.innerWidth - width - 12));
    // 哪边空间大开哪边；高度永远钳在所选侧的可用空间内（超出内部滚动），任何窗口尺寸都不会被裁。
    const margin = 14;
    const spaceAbove = r.top - margin;
    const spaceBelow = window.innerHeight - r.bottom - margin;
    if (spaceBelow > spaceAbove) {
      menuStyle = `top:${Math.round(r.bottom + 6)}px; left:${Math.round(left)}px; width:${width}px; max-height:${Math.round(spaceBelow - 6)}px;`;
    } else {
      menuStyle = `bottom:${Math.round(window.innerHeight - r.top + 6)}px; left:${Math.round(left)}px; width:${width}px; max-height:${Math.round(spaceAbove - 6)}px;`;
    }
  }
  function toggle() { open = !open; if (open) { requestAnimationFrame(position); void loadUsage(); } }

  // 本地用量参考（近 5 小时 / 今日，按 brain 汇总）：打开面板时拉一次。
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
  function pick(o: Opt) {
    if (o.disabled || lockModel) return;
    if (o.value !== value) onpick(o.value);
  }
  function setIdx(i: number) {
    const v = STOPS[Math.max(0, Math.min(STOPS.length - 1, i))].v;
    if (v !== effort) oneffort(v);
  }
  // 拖动：pointer capture，拖动中 1:1 跟手（不吸附），松手才落到最近档（带缓动）。
  function rawPct(clientX: number): number {
    if (!track) return 0;
    const r = track.getBoundingClientRect();
    return Math.max(0, Math.min(1, (clientX - r.left) / r.width)) * 100;
  }
  function onDown(e: PointerEvent) {
    dragging = true;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    dragPct = rawPct(e.clientX);
  }
  function onMove(e: PointerEvent) { if (dragging) dragPct = rawPct(e.clientX); }
  function onUp() {
    if (!dragging) return;
    dragging = false;
    if (dragPct !== null) setIdx(Math.round((dragPct / 100) * (STOPS.length - 1)));
    dragPct = null; // 回到档位定位，transition 重新生效＝松手吸附缓动
  }

  function onDoc(e: MouseEvent) { if (root && !root.contains(e.target as Node)) open = false; }
  function onKey(e: KeyboardEvent) { if (e.key === 'Escape') open = false; }
  // 菜单自身内部滚动不关（模型多时列表可滚）；页面滚动才关。
  function onScroll(e: Event) {
    const menu = root?.querySelector('.fx-menu');
    if (menu && (e.target === menu || (menu as HTMLElement).contains(e.target as Node))) return;
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
</script>

<div class="fx" bind:this={root}>
  <button type="button" class="fx-trigger" class:open onclick={toggle}>
    {#if current?.icon}<BrainIcon brain={current.icon} size={13} />{/if}
    <span class="fx-label">{current?.label ?? (value || '模型')}</span>
    {#if idx > 0}<span class="fx-eff">{STOPS[idx].l}</span>{/if}
    <svg class="fx-chev" width="10" height="10" viewBox="0 0 12 12" fill="none"><path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
  </button>
  {#if open}
    <div class="fx-menu" style={menuStyle} data-no-drag>
      <div class="fx-sec">模型{#if lockModel}<span class="fx-sec-sub">本轮进行中不可换</span>{/if}</div>
      <div class="fx-opts">
        {#each options as o (o.value)}
          <button type="button" class="fx-opt" class:on={o.value === value} disabled={o.disabled || lockModel} onclick={() => pick(o)}>
            {#if o.icon}<BrainIcon brain={o.icon} size={14} />{/if}
            <span class="fx-otext"><b>{o.label}</b>{#if o.sub}<small>{o.sub}</small>{/if}</span>
            {#if o.value === value}<span class="fx-check">✓</span>{/if}
          </button>
        {/each}
      </div>
      <div class="fx-div"></div>
      <div class="fx-sec">推理强度<span class="fx-sec-sub">{STOPS[liveIdx].l}{STOPS[liveIdx].d ? ' · ' + STOPS[liveIdx].d : ''}</span></div>
      <div class="fx-ends"><span>更快</span><span>更缜密</span></div>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="fx-slider" class:drag={dragging} class:max={pct >= 99.5} style="--p:{pct}" bind:this={track} onpointerdown={onDown} onpointermove={onMove} onpointerup={onUp} onpointercancel={onUp}>
        <span class="fx-fill"></span>
        {#each STOPS as st, i (st.v)}<span class="fx-dot" class:last={i === STOPS.length - 1} style="left:calc(11px + (100% - 22px) * {i} / {STOPS.length - 1})"></span>{/each}
        <span class="fx-fx">
          <span class="px px-a"></span>
          <span class="px px-b"></span>
          <span class="px px-c"></span>
          <span class="fx-sweep"></span>
        </span>
        <span class="fx-thumb"></span>
      </div>
      {#if usage}
        <div class="fx-div"></div>
        <div class="fx-usage">
          <div class="fx-usage-title">本地用量<span>token · 仅供参考</span></div>
          <div class="fx-usage-row"><span>近 5 小时</span><b>Claude {fmtTok(usage.window_a?.claude)} · Codex {fmtTok(usage.window_a?.codex)}</b></div>
          <div class="fx-usage-row"><span>今日</span><b>Claude {fmtTok(usage.window_b?.claude)} · Codex {fmtTok(usage.window_b?.codex)}</b></div>
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .fx { position: relative; display: inline-flex; }
  .fx-trigger { display: inline-flex; align-items: center; gap: 5px; padding: 3px 7px; border: none; border-radius: 7px; background: transparent; font-size: 12px; color: var(--dim, #6b675f); cursor: pointer; white-space: nowrap; }
  .fx-trigger:hover, .fx-trigger.open { background: #f1efe9; color: var(--text, #26241f); }
  .fx-label { max-width: 160px; overflow: hidden; text-overflow: ellipsis; }
  .fx-eff { font-size: 10.5px; padding: 0 6px; border-radius: 999px; background: #edece7; color: var(--text, #26241f); }
  .fx-chev { opacity: .6; flex: none; }

  .fx-menu { position: fixed; z-index: 95; display: flex; flex-direction: column; background: #fff; border: 1px solid var(--border2, #e2dfd7); border-radius: 12px; box-shadow: 0 12px 32px rgba(30, 25, 15, .16); padding: 7px; }
  .fx-sec { display: flex; align-items: center; justify-content: space-between; font-size: 10.5px; font-weight: 600; letter-spacing: .04em; color: var(--faint, #9b968c); padding: 4px 8px 5px; }
  .fx-sec-sub { font-weight: 500; letter-spacing: 0; color: var(--dim, #6b675f); }
  /* 菜单整体限高（position() 注入 max-height），只有模型列表这一段伸缩滚动——
     滑杆/用量常驻可见，也不会出现外层+列表的嵌套双滚动条。 */
  .fx-opts { flex: 1 1 auto; min-height: 0; max-height: 250px; overflow-y: auto; overscroll-behavior: contain; }
  .fx-opt { width: 100%; display: flex; align-items: center; gap: 8px; padding: 6px 8px; border: none; border-radius: 8px; background: transparent; text-align: left; cursor: pointer; font: inherit; }
  .fx-opt:hover:not(:disabled) { background: #f4f3ef; }
  .fx-opt.on { background: #efeee9; }
  .fx-opt:disabled { opacity: .45; cursor: not-allowed; }
  .fx-otext { display: flex; flex-direction: column; min-width: 0; }
  .fx-otext b { font-weight: 550; font-size: 12.5px; color: var(--text, #26241f); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .fx-otext small { font-size: 10.5px; color: var(--faint, #9b968c); }
  .fx-check { margin-left: auto; font-size: 12px; color: var(--text, #26241f); }
  .fx-div { height: 1px; margin: 5px 4px; background: var(--border, #ecebe6); }
  .fx-usage { padding: 2px 8px 4px; }
  .fx-usage-title { display: flex; align-items: center; justify-content: space-between; font-size: 10.5px; font-weight: 600; letter-spacing: .04em; color: var(--faint, #9b968c); padding: 2px 0 4px; }
  .fx-usage-title span { font-weight: 400; letter-spacing: 0; }
  .fx-usage-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 11.5px; color: var(--dim, #6b675f); padding: 1.5px 0; }
  .fx-usage-row b { font-weight: 500; color: var(--text, #26241f); white-space: nowrap; }

  .fx-ends { display: flex; justify-content: space-between; padding: 0 10px 4px; font-size: 10.5px; color: var(--faint, #9b968c); }
  /* 粗胶囊轨道：底为浅灰点阵（右侧渐显），填充为暖色渐变叠白色星点 */
  .fx-slider {
    position: relative; height: 30px; margin: 0 8px 6px; border-radius: 999px; overflow: hidden;
    cursor: pointer; touch-action: none; background: #eceae3;
  }
  /* Claude 同款构造（预览页实拍调定）：轨道底＝平滑的灰→暖红渐变，其上叠"马赛克调制"层——
     288×26 程序生成 SVG，4px 网格 3px 像素，左 18% 留白给纯渐变，之后密度 0→72% 递增；
     像素三种角色：白(提亮)/深红(压暗)/暖橙(高光)，低不透明度叠在渐变上浑然一体。
     A 常显；B 仅拖到底时与 A 交替明灭＝星尘闪烁。 */
  .fx-slider {
    position: relative; height: 26px; margin: 0 8px 6px; border-radius: 999px; overflow: hidden;
    cursor: pointer; touch-action: none; background: #eceae4;
  }
  /* 非到底＝极简模式：实心浅灰填充到滑块 + 剩余档位圆点（终点一颗品牌色） */
  .fx-fill {
    position: absolute; left: 0; top: 0; bottom: 0; border-radius: 999px; background: #d9d5cc;
    width: calc(2px + (100% - 4px) * var(--p) / 100 + 11px);
    transition: width .14s ease-out, opacity .2s;
  }
  .fx-slider.drag .fx-fill { transition: opacity .2s; }
  .fx-slider.max .fx-fill { opacity: 0; }
  .fx-dot { position: absolute; top: 50%; width: 5px; height: 5px; border-radius: 50%; background: #b8b3a9; transform: translate(-50%, -50%); }
  .fx-dot.last { background: #a03c2b; }
  /* 到底＝像素溶解模式：渐变底+像素+扫光整组淡入 */
  .fx-fx {
    position: absolute; inset: 0; border-radius: inherit; opacity: 0; transition: opacity .25s ease-out; pointer-events: none;
    background: linear-gradient(90deg, #d9d6ce 0%, #cfc4b8 32%, #c98d70 58%, #b05a3c 80%, #a03c2b 100%);
  }
  .fx-slider.max .fx-fx { opacity: 1; }
  .px { position: absolute; inset: 0; border-radius: inherit; pointer-events: none; background-size: 100% 100%; background-repeat: no-repeat; }
  .px-a { background-image: url("data:image/svg+xml,%3Csvg%20xmlns%3D'http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg'%20width%3D'288'%20height%3D'26'%3E%3Crect%20x%3D'88'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'96'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'100'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'100'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'104'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'116'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'120'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'124'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'124'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'124'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'124'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'128'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'128'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'132'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'132'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.58'%2F%3E%3Crect%20x%3D'132'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'140'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'144'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'144'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'152'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'152'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'156'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'160'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'164'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'164'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'168'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.59'%2F%3E%3Crect%20x%3D'172'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'172'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.33'%2F%3E%3Crect%20x%3D'176'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'180'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'180'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'184'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'184'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'184'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'196'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'196'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'200'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.28'%2F%3E%3Crect%20x%3D'200'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'200'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'204'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'204'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'204'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'208'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'208'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'208'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'212'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'212'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'212'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.57'%2F%3E%3Crect%20x%3D'216'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'216'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.58'%2F%3E%3Crect%20x%3D'220'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'220'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'220'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'220'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'224'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'224'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'224'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'224'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'228'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'228'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'228'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'228'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'228'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'232'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'232'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'232'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.28'%2F%3E%3Crect%20x%3D'236'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'236'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'236'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'236'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'236'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'240'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'240'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'240'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'240'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.33'%2F%3E%3Crect%20x%3D'240'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'244'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.59'%2F%3E%3Crect%20x%3D'244'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'244'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'248'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'252'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'252'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'252'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'252'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'256'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'256'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'256'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.33'%2F%3E%3Crect%20x%3D'256'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'256'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'260'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'260'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'260'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'260'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.25'%2F%3E%3Crect%20x%3D'264'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'264'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'264'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.56'%2F%3E%3Crect%20x%3D'264'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'268'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'268'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'268'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'268'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.33'%2F%3E%3Crect%20x%3D'272'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'272'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'272'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'272'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'272'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'276'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'276'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'276'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'276'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'280'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'280'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'280'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'280'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'284'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'284'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'284'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'284'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.57'%2F%3E%3Crect%20x%3D'284'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.35'%2F%3E%3C%2Fsvg%3E"); }
  .px-b { background-image: url("data:image/svg+xml,%3Csvg%20xmlns%3D'http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg'%20width%3D'288'%20height%3D'26'%3E%3Crect%20x%3D'80'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'92'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'100'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'104'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'120'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'124'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'128'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'128'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'128'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'128'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'136'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'148'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'148'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'152'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'152'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'156'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'156'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'160'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'160'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'160'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'164'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'164'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'172'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'172'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'176'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'176'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'176'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'180'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'180'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'188'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'188'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'192'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'192'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'192'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'196'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'196'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.59'%2F%3E%3Crect%20x%3D'200'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'200'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'204'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'204'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'204'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'204'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'208'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'212'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'212'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'212'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'212'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'216'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'220'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'220'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'220'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'224'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'224'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'228'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'232'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'232'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'232'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'236'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'236'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'240'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'240'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.57'%2F%3E%3Crect%20x%3D'240'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'240'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'244'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'244'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'244'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'248'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'252'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'252'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'256'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'256'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'256'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'256'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'260'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'260'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'260'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'264'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'264'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'268'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'268'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'268'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'272'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'272'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'272'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'272'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'276'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'280'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'280'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'280'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.56'%2F%3E%3Crect%20x%3D'280'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.26'%2F%3E%3Crect%20x%3D'284'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.60'%2F%3E%3Crect%20x%3D'284'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'284'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'284'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'284'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.39'%2F%3E%3C%2Fsvg%3E"); opacity: 0; }
  .px-c { background-image: url("data:image/svg+xml,%3Csvg%20xmlns%3D'http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg'%20width%3D'288'%20height%3D'26'%3E%3Crect%20x%3D'84'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'88'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'112'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'112'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'116'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'120'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'124'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'128'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'136'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'140'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'140'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.31'%2F%3E%3Crect%20x%3D'144'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'144'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'144'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'144'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'148'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'152'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'156'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'160'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'160'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.28'%2F%3E%3Crect%20x%3D'164'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'168'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'168'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'172'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'172'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'172'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'176'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.33'%2F%3E%3Crect%20x%3D'176'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'176'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.55'%2F%3E%3Crect%20x%3D'176'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'180'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'184'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'184'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.28'%2F%3E%3Crect%20x%3D'188'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'196'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'196'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'196'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'200'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.36'%2F%3E%3Crect%20x%3D'204'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.59'%2F%3E%3Crect%20x%3D'204'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'204'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'208'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'208'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'212'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'212'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'212'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'216'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.58'%2F%3E%3Crect%20x%3D'216'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'216'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.28'%2F%3E%3Crect%20x%3D'216'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.42'%2F%3E%3Crect%20x%3D'220'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'220'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'220'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'224'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'224'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.33'%2F%3E%3Crect%20x%3D'224'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'228'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.28'%2F%3E%3Crect%20x%3D'228'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'232'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.32'%2F%3E%3Crect%20x%3D'232'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'232'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'232'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.40'%2F%3E%3Crect%20x%3D'236'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.35'%2F%3E%3Crect%20x%3D'236'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.54'%2F%3E%3Crect%20x%3D'236'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'240'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'240'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'240'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'240'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'244'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'244'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.38'%2F%3E%3Crect%20x%3D'244'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.45'%2F%3E%3Crect%20x%3D'248'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'248'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'252'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'252'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.29'%2F%3E%3Crect%20x%3D'256'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'256'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.30'%2F%3E%3Crect%20x%3D'256'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'260'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.53'%2F%3E%3Crect%20x%3D'260'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.39'%2F%3E%3Crect%20x%3D'260'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.41'%2F%3E%3Crect%20x%3D'260'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.25'%2F%3E%3Crect%20x%3D'260'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'264'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.34'%2F%3E%3Crect%20x%3D'264'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'264'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.46'%2F%3E%3Crect%20x%3D'264'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'268'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'268'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.27'%2F%3E%3Crect%20x%3D'268'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'268'%20y%3D'20'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.44'%2F%3E%3Crect%20x%3D'272'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'272'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'272'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'272'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'276'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.43'%2F%3E%3Crect%20x%3D'276'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.49'%2F%3E%3Crect%20x%3D'276'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.51'%2F%3E%3Crect%20x%3D'276'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.25'%2F%3E%3Crect%20x%3D'276'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffffff'%20opacity%3D'0.37'%2F%3E%3Crect%20x%3D'280'%20y%3D'0'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'280'%20y%3D'4'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.52'%2F%3E%3Crect%20x%3D'280'%20y%3D'12'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.47'%2F%3E%3Crect%20x%3D'280'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.50'%2F%3E%3Crect%20x%3D'284'%20y%3D'8'%20width%3D'3'%20height%3D'3'%20fill%3D'%237d2213'%20opacity%3D'0.48'%2F%3E%3Crect%20x%3D'284'%20y%3D'16'%20width%3D'3'%20height%3D'3'%20fill%3D'%23ffb27a'%20opacity%3D'0.44'%2F%3E%3C%2Fsvg%3E"); opacity: 0; }
  /* 拖到底的持续动效（对标 GPT/Claude）：三帧像素图相位错开地此起彼伏＝星星连续闪烁，
     再加一道暖白高光每 2.2s 扫过。 */
  .fx-slider.max .px-a { animation: fxga 2.6s ease-in-out infinite; }
  .fx-slider.max .px-b { animation: fxgb 2.6s ease-in-out infinite; }
  .fx-slider.max .px-c { animation: fxgb 2.6s ease-in-out infinite; animation-delay: -1.3s; }
  @keyframes fxga { 0%, 100% { opacity: 1; } 50% { opacity: .45; } }
  @keyframes fxgb { 0%, 100% { opacity: 0; } 50% { opacity: .9; } }
  .fx-sweep { position: absolute; inset: 0; border-radius: inherit; pointer-events: none; }
  .fx-slider.max .fx-sweep {
    background: linear-gradient(105deg, transparent 34%, rgba(255, 255, 255, .28) 46%, rgba(255, 224, 190, .42) 50%, rgba(255, 255, 255, .28) 54%, transparent 66%);
    animation: fxsweep 2.2s ease-in-out infinite;
  }
  @keyframes fxsweep { 0% { transform: translateX(-72%); } 62%, 100% { transform: translateX(72%); } }
  @media (prefers-reduced-motion: reduce) {
    .fx-slider.max .px-a, .fx-slider.max .px-b, .fx-slider.max .px-c, .fx-slider.max .fx-sweep { animation: none; }
    .fx-slider.max .fx-sweep { background: none; }
  }
  /* 白色大圆钮：left+translateX(-p%) 让它始终滑动在胶囊内侧（0%贴左缘、100%贴右缘） */
  .fx-thumb {
    position: absolute; top: 50%; z-index: 2; width: 22px; height: 22px;
    left: calc(2px + (100% - 4px) * var(--p) / 100); /* 26px 轨道内缩 2px：滑块贴壁滑动 */
    transform: translate(calc(var(--p) * -1%), -50%);
    border-radius: 50%;
    background: linear-gradient(180deg, #ffffff, #f4f2ec);
    box-shadow: inset 0 0 0 1px rgba(30, 25, 15, .07), 0 1px 2px rgba(30, 25, 15, .14), 0 3px 8px rgba(30, 25, 15, .12);
    transition: left .14s ease-out; pointer-events: none;
  }
  .fx-slider.drag .fx-thumb { transition: none; }
</style>
