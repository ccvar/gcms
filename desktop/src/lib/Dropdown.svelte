<script lang="ts">
  import BrainIcon from './BrainIcon.svelte';
  interface Opt { value: string; label: string; sub?: string; disabled?: boolean; icon?: string; img?: string; }
  // 站点图标：有 logo 用 logo，否则用 slug 首字母做圆形占位。

  let {
    value = $bindable(),
    options,
    placeholder = '选择…',
    disabled = false,
    compact = false,
    searchable = false,
    onchange,
  }: { value: string; options: Opt[]; placeholder?: string; disabled?: boolean; compact?: boolean; searchable?: boolean; onchange?: (value: string) => void } = $props();

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
    const width = compact ? Math.max(r.width, 240) : r.width;
    let left = r.left;
    if (compact) left = Math.max(12, Math.min(left, window.innerWidth - width - 12));
    const vert = up
      ? `bottom:${Math.round(window.innerHeight - r.top + 6)}px`
      : `top:${Math.round(r.bottom + 6)}px`;
    menuStyle = `${vert}; left:${Math.round(left)}px; width:${Math.round(width)}px; max-height:${Math.round(maxH)}px;`;
  }
  function toggle() { if (disabled) return; open = !open; if (open) { query = ''; requestAnimationFrame(() => { position(); if (showSearch) searchEl?.focus(); }); } }
  function pick(o: Opt) { if (o.disabled) return; const changed = o.value !== value; value = o.value; open = false; if (changed) onchange?.(o.value); }
  function onDoc(e: MouseEvent) { if (root && !root.contains(e.target as Node)) open = false; }
  function onKey(e: KeyboardEvent) { if (e.key === 'Escape') open = false; }
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

<div class="dd" class:compact bind:this={root}>
  <button type="button" class="dd-trigger" class:open class:compact onclick={toggle} {disabled}>
    <span class="dd-label" class:placeholder={!current}>
      {#if current}{@render lead(current)}{/if}{current?.label ?? placeholder}
    </span>
    <svg class="dd-chev" class:up={open} width="12" height="12" viewBox="0 0 12 12" fill="none">
      <path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  </button>
  {#if open}
    <div class="dd-menu" style={menuStyle}>
      {#if showSearch}
        <div class="dd-search">
          <input bind:this={searchEl} bind:value={query} placeholder="搜索站点…" spellcheck="false" autocapitalize="off" autocorrect="off" />
        </div>
      {/if}
      {#each filtered as o (o.value)}
        <button type="button" class="dd-opt" class:sel={o.value === value} class:disabled={o.disabled} onclick={() => pick(o)}>
          {@render lead(o)}
          <span class="dd-otext"><b>{o.label}</b>{#if o.sub}<small>{o.sub}</small>{/if}</span>
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
  .dd-trigger {
    width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 8px;
    background: #fff; border: 1px solid var(--border2, #e1dfd8); border-radius: 10px;
    padding: 7px 11px; font: inherit; font-size: 14px; color: var(--text, #26241f); cursor: pointer; text-align: left;
  }
  .dd-trigger:hover { border-color: #cfccc2; }
  .dd-trigger.open { border-color: #b7b2a6; box-shadow: none; }
  /* 紧凑 chip：无边框、透明底、自适应宽，用于输入框底栏。 */
  .dd-trigger.compact { width: auto; gap: 5px; padding: 4px 8px; font-size: 13px; border-color: transparent; background: transparent; border-radius: 8px; max-width: 200px; }
  .dd-trigger.compact:hover { background: #f1efe9; border-color: transparent; }
  .dd-trigger.compact.open { background: #ecebe6; border-color: transparent; }
  .dd-trigger:disabled { opacity: .55; cursor: default; }
  .dd-label { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .dd-label :global(.bi) { flex: none; }
  .dd-fav { width: 17px; height: 17px; border-radius: 5px; flex: none; object-fit: contain; background: #f0eee9; }
  .dd-fav.ph { display: inline-flex; align-items: center; justify-content: center; font-size: 10px; font-weight: 600; color: var(--dim, #6f6b62); border: none; background: #e7e4dd; }
  .dd-label.placeholder { color: var(--faint, #a29d93); }
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
  .dd-check { color: var(--accent, #4f46e5); flex: none; font-size: 13px; margin-left: auto; }
  .dd-search { position: sticky; top: 0; z-index: 1; background: #fff; padding: 2px 2px 5px; margin-bottom: 2px; border-bottom: 1px solid var(--border, #ecebe6); }
  .dd-search input { width: 100%; border: none; outline: none; background: #f4f3ef; border-radius: 7px; padding: 6px 9px; font: inherit; font-size: 13px; color: var(--text, #26241f); }
  .dd-empty { padding: 14px 12px; text-align: center; color: var(--faint, #a29d93); font-size: 12.5px; }
</style>
