<script lang="ts">
  import BrainIcon from './BrainIcon.svelte';
  // tip：选项的长说明，挂 data-tip 交给全局 fixed 浮层（+page.svelte 的 onTipHover 用
  // closest('[title],[data-tip]') 代理，共享 .ui-tip 会按真实尺寸避让视口并保留 \n）—— 比塞进
  // sub 强：菜单 compact 时才 240px 宽，长文案会把那一行撑成七八行。
  // tone：给 sub 上风险色（'danger'/'warn'）。组件原有的 tone 只染触发器，够不到菜单里的选项行。
  interface Opt { value: string; label: string; short?: string; sub?: string; disabled?: boolean; icon?: string; img?: string; tip?: string; tone?: string; }
  // 站点图标：有 logo 用 logo，否则用 slug 首字母做圆形占位。

  let {
    value = $bindable(),
    options,
    placeholder = '选择…',
    disabled = false,
    compact = false,
    menuCompact = false,
    searchable = false,
    tone = '',
    bare = false,
    tip = '',
    onchange,
  }: { value: string; options: Opt[]; placeholder?: string; disabled?: boolean; compact?: boolean; menuCompact?: boolean; searchable?: boolean; tone?: string; bare?: boolean; tip?: string; onchange?: (value: string) => void } = $props();

  let open = $state(false);
  let root: HTMLDivElement | undefined = $state();
  let menuStyle = $state('');
  let failed = $state(new Set<string>());
  let query = $state('');
  let searchEl: HTMLInputElement | null = $state(null);
  function markFailed(src: string) { const n = new Set(failed); n.add(src); failed = n; }
  const current = $derived(options.find((o) => o.value === value));
  // 选项多时在菜单顶部给搜索框，按名称/子标题（站点域名）过滤。
  const showSearch = $derived(searchable && options.length > 6);
  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((o) => o.label.toLowerCase().includes(q) || (o.sub ?? '').toLowerCase().includes(q) || o.value.toLowerCase().includes(q));
  });

  // 菜单用 fixed 定位（坐标取自触发器），这样不会被弹窗/滚动容器的 overflow 裁掉。
  function position() {
    if (!root) return;
    const btn = root.querySelector('.dd-trigger') as HTMLElement | null;
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    // 下方空间不足且上方更宽裕时向上翻，避免菜单被窗口底边裁掉（输入框底栏的下拉会靠底）。
    const belowSpace = window.innerHeight - r.bottom - 12;
    const aboveSpace = r.top - 12;
    const up = belowSpace < 220 && aboveSpace > belowSpace;
    const maxH = Math.min(300, Math.max(96, up ? aboveSpace : belowSpace));
    // 紧凑模式触发器很窄，菜单加宽到 ≥240px；靠右时钳制左边界防溢出。
    const width = menuCompact ? Math.max(r.width, 132) : compact ? Math.max(r.width, 240) : r.width;
    let left = r.left;
    if (compact) left = Math.max(12, Math.min(left, window.innerWidth - width - 12));
    const vert = up
      ? `bottom:${Math.round(window.innerHeight - r.top + 6)}px`
      : `top:${Math.round(r.bottom + 6)}px`;
    menuStyle = `${vert}; left:${Math.round(left)}px; width:${Math.round(width)}px; max-height:${Math.round(maxH)}px;`;
  }
  // 开菜单时把触发器那个气泡收掉。光「把 data-tip 摘了」不够 —— 气泡是**悬停那一刻**就弹出来的，
  // 全局那套（+page.svelte 的 svelte:window onmouseover）只在**下一次 mouseover**才重算，
  // 没有 mouseleave 兜底；鼠标停在触发器上不动，气泡就一直挂着，正好盖住菜单最下面那条
  // （实测盖的就是「全自动」）。往 body 补一次 mouseover 走同一条代理路径，把它清掉。
  function hideTipBubble() {
    document.body.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
  }
  function toggle() { if (disabled) return; open = !open; if (open) { query = ''; hideTipBubble(); requestAnimationFrame(() => { position(); if (showSearch) searchEl?.focus(); }); } }
  // pick / Escape 关菜单时也得收气泡：选项自己带 data-tip，悬停它会弹气泡；点中/按 Esc 后
  // 菜单卸载、光标却没动 → 不会有新的 mouseover 来清它，气泡就飘在原地。onDoc（点外面）和
  // onScroll 本身带移动/滚动，会自己清，不用管。
  function pick(o: Opt) { if (o.disabled) return; const changed = o.value !== value; value = o.value; open = false; hideTipBubble(); if (changed) onchange?.(o.value); }
  function onDoc(e: MouseEvent) { if (root && !root.contains(e.target as Node)) open = false; }
  function onKey(e: KeyboardEvent) { if (e.key === 'Escape') { open = false; hideTipBubble(); } }
  // 只在「菜单之外」的滚动才收起；菜单自身内部滚动（选项多时）不关闭。
  function onScroll(e: Event) {
    const menu = root?.querySelector('.dd-menu');
    if (menu && (e.target === menu || menu.contains(e.target as Node))) return;
    open = false;
  }

  $effect(() => {
    if (!open) return;
    // 捕获阶段：先于拖拽区/触发器的 mousedown 处理，点空白（含 data-tauri-drag-region）也能关闭。
    document.addEventListener('mousedown', onDoc, true);
    document.addEventListener('keydown', onKey);
    // 任意祖先滚动就关闭（fixed 菜单不会跟着滚）。
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onScroll);
    return () => {
      document.removeEventListener('mousedown', onDoc, true);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onScroll);
    };
  });
</script>

<div class="dd" class:compact class:bare bind:this={root}>
  <!-- data-tip 挂触发器本体：.dd-menu 是它的**兄弟**不是后代，所以菜单里的选项 closest 不会
       误捞到这条。
       ★ 菜单开着时必须把它摘掉：气泡是 z-index:130、菜单才 90，两个又都往上弹 —— 不摘的话
       气泡正好盖住菜单最下面那条（实测盖的就是「全自动」，即最要紧的那条）。
       同款做法见 UsageRing.svelte 的 `use:tip={open ? '' : tipText}`。 -->
  <button type="button" class="dd-trigger" class:open class:compact class:bare class:tone-warn={tone === 'warn'} class:tone-danger={tone === 'danger'} onclick={toggle} {disabled} data-tip={open ? null : tip || null}>
    <span class="dd-label" class:placeholder={!current}>
      {#if current}{@render lead(current)}{/if}{current?.short ?? current?.label ?? placeholder}
    </span>
    <svg class="dd-chev" class:up={open} width="12" height="12" viewBox="0 0 12 12" fill="none">
      <path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  </button>
  {#if open}
    <div class="dd-menu" class:menu-compact={menuCompact} style={menuStyle}>
      {#if showSearch}
        <div class="dd-search">
          <input bind:this={searchEl} bind:value={query} placeholder="搜索站点…" spellcheck="false" autocapitalize="off" autocorrect="off" />
        </div>
      {/if}
      {#each filtered as o (o.value)}
        <button type="button" class="dd-opt" class:sel={o.value === value} class:disabled={o.disabled} onclick={() => pick(o)} data-tip={o.tip || null}>
          {@render lead(o)}
          <span class="dd-otext"><b>{o.label}</b>{#if o.sub}<small class:danger={o.tone === 'danger'} class:warn={o.tone === 'warn'}>{o.sub}</small>{/if}</span>
          {#if o.value === value}<span class="dd-check">✓</span>{/if}
        </button>
      {/each}
      {#if filtered.length === 0}<div class="dd-empty">无匹配站点</div>{/if}
    </div>
  {/if}
</div>

{#snippet lead(o: Opt)}
  {#if o.icon}<BrainIcon brain={o.icon} size={16} />
  {:else if o.img && !failed.has(o.img)}<img class="dd-fav" src={o.img} alt="" loading="lazy" onerror={() => markFailed(o.img!)} />
  {:else if 'img' in o}<span class="dd-fav ph">{(o.label || '?').slice(0, 1).toUpperCase()}</span>{/if}
{/snippet}

<style>
  .dd { position: relative; width: 100%; }
  .dd.compact { width: auto; }
  /* 表单控件统一尺度（与 .tin/.btn 同基准）：13px 字 + 8px 圆角 + 显式统一高 --ctl-h，
     内容 flex 垂直居中；compact/bare 是行内 chip 形态，height 回落 auto。 */
  .dd-trigger {
    width: 100%; height: var(--ctl-h, 30px); box-sizing: border-box;
    display: flex; align-items: center; justify-content: space-between; gap: 8px;
    background: #fff; border: 1px solid var(--border2, #e1dfd8); border-radius: 8px;
    padding: 0 10px; font: inherit; font-size: 13px; color: var(--text, #26241f); cursor: pointer; text-align: left;
  }
  .dd-trigger:hover { border-color: #cfccc2; }
  .dd-trigger.open { border-color: #b7b2a6; box-shadow: none; }
  /* 紧凑 chip：无边框、透明底、自适应宽，用于输入框底栏（--chip-h 定高对齐同栏其他 chip）。 */
  .dd-trigger.compact { width: auto; height: var(--chip-h, 24px); box-sizing: border-box; gap: 5px; padding: 0 8px; font-size: 13px; border-color: transparent; background: transparent; border-radius: 8px; max-width: 200px; }
  .dd-trigger.compact:hover { background: #f1efe9; border-color: transparent; }
  .dd-trigger.compact.open { background: #ecebe6; border-color: transparent; }
  .dd-trigger:disabled { opacity: .55; cursor: default; }
  .dd-label { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .dd-label :global(.bi) { flex: none; }
  /* 有真实图标不垫底色；底色只留给字母占位（.ph） */
  .dd-fav { width: 17px; height: 17px; border-radius: 5px; flex: none; object-fit: contain; background: transparent; }
  .dd-fav.ph { display: inline-flex; align-items: center; justify-content: center; font-size: 10px; font-weight: 600; color: var(--dim, #6f6b62); border: none; background: #e7e4dd; }
  .dd-label.placeholder { color: var(--faint, #a29d93); }
  /* 权限档位等风险提示：把当前选中值文字染成警告/危险色（含 chevron） */
  /* bare：无边框无底色无内边距的安静文字触发器（排期筛选等场景，与周边文字同线） */
  .dd.bare { width: auto; }
  .dd-trigger.bare { border: none; background: transparent; padding: 2px 4px 2px 0; width: auto; height: auto; font-size: 12.5px; color: var(--dim, #6b675f); box-shadow: none; }
  .dd-trigger.bare:hover { background: transparent; color: var(--text, #26241f); }
  .dd-trigger.bare:focus, .dd-trigger.bare:focus-visible { outline: none; box-shadow: none; }
  .dd-trigger.bare.open { background: transparent; border-color: transparent; box-shadow: none; }
  .dd-trigger.tone-warn { color: var(--warn, #b45309); }
  .dd-trigger.tone-warn .dd-label, .dd-trigger.tone-warn .dd-chev { color: var(--warn, #b45309); }
  .dd-trigger.tone-danger { color: var(--err, #dc2626); }
  .dd-trigger.tone-danger .dd-label, .dd-trigger.tone-danger .dd-chev { color: var(--err, #dc2626); }
  .dd-chev { color: var(--faint, #a29d93); flex: none; transition: transform .15s; }
  .dd-chev.up { transform: rotate(180deg); }

  .dd-menu {
    position: fixed; z-index: 90;
    background: #fff; border: 1px solid var(--border, #ecebe6); border-radius: 12px;
    box-shadow: 0 12px 32px rgba(30,25,15,.14); padding: 5px; max-height: 280px; overflow-y: auto;
    animation: pop .1s ease-out;
  }
  @keyframes pop { from { opacity: 0; transform: translateY(-4px); } }
  .dd-opt {
    width: 100%; display: flex; align-items: center; gap: 9px;
    background: none; border: none; border-radius: 8px; padding: 7px 10px; cursor: pointer; text-align: left; font: inherit;
    color: var(--text, #26241f);
  }
  .dd-opt:hover { background: #f4f3ef; }
  .dd-opt.sel { background: #efeee9; }
  .dd-opt.disabled { opacity: .4; cursor: default; }
  .dd-opt.disabled:hover { background: none; }
  .dd-otext { display: flex; flex-direction: column; gap: 0; min-width: 0; flex: 1; }
  .dd-otext b { font-weight: 500; font-size: 13.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .dd-otext small { color: var(--dim, #6f6b62); font-size: 11.5px; line-height: 1.3; }
  /* 风险档位的副标题上色（组件顶上那个 tone 只染触发器，够不到菜单里的选项行） */
  .dd-otext small.danger { color: var(--err, #dc2626); }
  .dd-otext small.warn { color: var(--warn, #b45309); }
  .dd-check { color: var(--accent, #33302a); flex: none; font-size: 13px; margin-left: auto; }
  .dd-menu.menu-compact { padding: 3px; border-radius: 9px; }
  .dd-menu.menu-compact .dd-opt { min-height: 27px; gap: 6px; padding: 4px 8px; border-radius: 6px; }
  .dd-menu.menu-compact .dd-otext b { font-size: 12px; line-height: 1.15; }
  .dd-menu.menu-compact .dd-check { font-size: 11px; }
  .dd-search { position: sticky; top: 0; z-index: 1; background: #fff; padding: 2px 2px 5px; margin-bottom: 2px; border-bottom: 1px solid var(--border, #ecebe6); }
  .dd-search input { width: 100%; border: none; outline: none; background: #f4f3ef; border-radius: 7px; padding: 6px 9px; font: inherit; font-size: 13px; color: var(--text, #26241f); }
  .dd-empty { padding: 14px 12px; text-align: center; color: var(--faint, #a29d93); font-size: 12.5px; }
</style>
