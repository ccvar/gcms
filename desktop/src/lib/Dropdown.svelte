<script lang="ts">
  import BrainIcon from './BrainIcon.svelte';
  interface Opt { value: string; label: string; sub?: string; disabled?: boolean; icon?: string; img?: string; }
  // 站点图标：有 logo 用 logo，否则用 slug 首字母做圆形占位。

  let {
    value = $bindable(),
    options,
    placeholder = '选择…',
    disabled = false,
  }: { value: string; options: Opt[]; placeholder?: string; disabled?: boolean } = $props();

  let open = $state(false);
  let root: HTMLDivElement | undefined = $state();
  let menuStyle = $state('');
  const current = $derived(options.find((o) => o.value === value));

  // 菜单用 fixed 定位（坐标取自触发器），这样不会被弹窗/滚动容器的 overflow 裁掉。
  function position() {
    if (!root) return;
    const btn = root.querySelector('.dd-trigger') as HTMLElement | null;
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    // 始终向下展开，高度自适应可用空间（不足则内部滚动），绝不向上翻盖住上方内容。
    const belowSpace = window.innerHeight - r.bottom - 12;
    const maxH = Math.min(300, Math.max(96, belowSpace));
    menuStyle = `top:${Math.round(r.bottom + 6)}px; left:${Math.round(r.left)}px; width:${Math.round(r.width)}px; max-height:${Math.round(maxH)}px;`;
  }
  function toggle() { if (disabled) return; open = !open; if (open) requestAnimationFrame(position); }
  function pick(o: Opt) { if (o.disabled) return; value = o.value; open = false; }
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
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('keydown', onKey);
    // 任意祖先滚动就关闭（fixed 菜单不会跟着滚）。
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onScroll);
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onScroll);
    };
  });
</script>

<div class="dd" bind:this={root}>
  <button type="button" class="dd-trigger" class:open onclick={toggle} {disabled}>
    <span class="dd-label" class:placeholder={!current}>
      {#if current}{@render lead(current)}{/if}{current?.label ?? placeholder}
    </span>
    <svg class="dd-chev" class:up={open} width="12" height="12" viewBox="0 0 12 12" fill="none">
      <path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  </button>
  {#if open}
    <div class="dd-menu" style={menuStyle}>
      {#each options as o (o.value)}
        <button type="button" class="dd-opt" class:sel={o.value === value} class:disabled={o.disabled} onclick={() => pick(o)}>
          {@render lead(o)}
          <span class="dd-otext"><b>{o.label}</b>{#if o.sub}<small>{o.sub}</small>{/if}</span>
          {#if o.value === value}<span class="dd-check">✓</span>{/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

{#snippet lead(o: Opt)}
  {#if o.icon}<BrainIcon brain={o.icon} size={16} />
  {:else if o.img}<img class="dd-fav" src={o.img} alt="" loading="lazy" />
  {:else if 'img' in o}<span class="dd-fav ph">{(o.label || '?').slice(0, 1).toUpperCase()}</span>{/if}
{/snippet}

<style>
  .dd { position: relative; width: 100%; }
  .dd-trigger {
    width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 8px;
    background: #fff; border: 1.5px solid var(--border2, #e1dfd8); border-radius: 10px;
    padding: 9px 11px; font: inherit; font-size: 14px; color: var(--text, #26241f); cursor: pointer; text-align: left;
  }
  .dd-trigger:hover { border-color: #cfccc2; }
  .dd-trigger.open { border-color: var(--accent, #4f46e5); box-shadow: 0 0 0 3px var(--accent-soft, #eef0fe); }
  .dd-trigger:disabled { opacity: .55; cursor: default; }
  .dd-label { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .dd-label :global(.bi) { flex: none; }
  .dd-fav { width: 17px; height: 17px; border-radius: 5px; flex: none; object-fit: contain; background: #f0eee9; border: 1px solid var(--border, #ecebe6); }
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
    width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 8px;
    background: none; border: none; border-radius: 8px; padding: 8px 10px; cursor: pointer; text-align: left; font: inherit;
    color: var(--text, #26241f);
  }
  .dd-opt:hover { background: #f4f3ef; }
  .dd-opt.sel { background: var(--accent-soft, #eef0fe); }
  .dd-opt.disabled { opacity: .4; cursor: default; }
  .dd-opt.disabled:hover { background: none; }
  .dd-otext { display: flex; flex-direction: column; gap: 0; min-width: 0; }
  .dd-otext b { font-weight: 500; font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .dd-otext small { color: var(--dim, #6f6b62); font-size: 11.5px; }
  .dd-check { color: var(--accent, #4f46e5); flex: none; font-size: 13px; }
</style>
