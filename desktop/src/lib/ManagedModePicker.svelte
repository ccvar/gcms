<script lang="ts">
  import { tick } from 'svelte';

  type Mode = 'plan' | 'growth';
  let {
    mode, label, disabled = false, onchange,
  }: { mode: Mode; label: string; disabled?: boolean; onchange: (mode: Mode) => void } = $props();

  let open = $state(false), ready = $state(false);
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();
  let menu = $state<HTMLDivElement>();
  let menuStyle = $state('');
  const options: { value: Mode; label: string }[] = [
    { value: 'plan', label: '计划托管' },
    { value: 'growth', label: '增长托管' },
  ];

  function close(restoreFocus = false) {
    open = false;
    if (restoreFocus) trigger?.focus({ preventScroll: true });
  }

  async function toggle() {
    if (disabled) return;
    if (open) { close(); return; }
    ready = false;
    open = true;
    document.body.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
    await tick();
    if (!open || !trigger || !menu) return;
    const r = trigger.getBoundingClientRect(), m = menu.getBoundingClientRect();
    const x = Math.max(8, Math.min(window.innerWidth - m.width - 8, r.left));
    const y = Math.max(8, Math.min(window.innerHeight - m.height - 8,
      window.innerHeight - r.bottom < m.height + 13 ? r.top - m.height - 5 : r.bottom + 5));
    menuStyle = `left:${x}px;top:${y}px`;
    ready = true;
    await tick();
    if (open) menu?.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus({ preventScroll: true });
  }

  function choose(next: Mode) {
    close(true);
    // The parent confirms and persists the change. Never update the badge optimistically:
    // a canceled dialog or failed save must keep the actual current mode visible.
    if (!disabled && next !== mode) onchange(next);
  }

  function onTriggerKey(e: KeyboardEvent) {
    if (!open && !disabled && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault();
      e.stopPropagation();
      void toggle();
    }
  }

  function onKey(e: KeyboardEvent) {
    if (!open || !root?.contains(e.target as Node)) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close(true);
      return;
    }
    if (e.key === 'Tab') { close(); return; }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    e.stopPropagation();
    const items = Array.from(menu?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]') ?? []);
    if (!items.length) return;
    const index = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1
      : e.key === 'ArrowDown' ? (index + 1) % items.length
        : (index - 1 + items.length) % items.length;
    items[next]?.focus();
  }

  $effect(() => {
    if (disabled) close();
  });

  $effect(() => {
    if (!open) return;
    const outside = (e: PointerEvent) => { if (!root?.contains(e.target as Node)) close(); };
    const onScroll = (e: Event) => { if (!menu?.contains(e.target as Node)) close(); };
    const onResize = () => close();
    document.addEventListener('pointerdown', outside, true);
    document.addEventListener('keydown', onKey);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', outside, true);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onResize);
    };
  });
</script>

<div class="mode-picker" bind:this={root}>
  <button type="button" class="trigger" class:growth={mode === 'growth'} class:active={open}
    bind:this={trigger} {disabled} aria-label={`托管模式：${label}`} aria-haspopup="menu" aria-expanded={open}
    data-tip={open ? null : '切换托管模式'} onclick={toggle} onkeydown={onTriggerKey}>
    <span class="label">{label}</span>
    <svg class="chevron" class:up={open} viewBox="0 0 12 12" fill="none" aria-hidden="true">
      <path d="M3 4.5 6 7.5 9 4.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  </button>
  {#if open}
    <div class="menu" class:ready bind:this={menu} style={menuStyle} role="menu" aria-label="切换托管模式">
      {#each options as option}
        <button type="button" role="menuitemradio" aria-checked={mode === option.value}
          class:selected={mode === option.value} onclick={() => choose(option.value)}>
          <svg class="mode-icon" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            {#if option.value === 'plan'}
              <path d="M5 2v3M11 2v3M3 6h10M4 3.5h8a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1Z"
                stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" />
            {:else}
              <path d="M2.5 12.5h11M3 10l3.2-3.2 2.3 2.3L13 4.5M9.5 4.5H13V8"
                stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
            {/if}
          </svg>
          <span>{option.label}</span>
          {#if mode === option.value}
            <svg class="check" viewBox="0 0 12 12" fill="none" aria-hidden="true">
              <path d="m2.5 6 2.3 2.3 4.7-4.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .mode-picker { display:inline-flex; align-items:center; min-width:0; }
  .trigger { display:inline-flex; align-items:center; justify-content:center; gap:4px; min-height:20px; padding:1px 7px; border:0; border-radius:999px; background:#f0eee8; color:var(--dim, #77736b); font:inherit; font-size:10.5px; line-height:1.2; white-space:nowrap; cursor:pointer; }
  .trigger.growth { background:#e8f2ec; color:#27704c; }
  .trigger:hover,.trigger.active { background:#e6e3dc; }
  .trigger.growth:hover,.trigger.growth.active { background:#dcece1; }
  .trigger:focus-visible { outline:2px solid var(--dim, #77736b); outline-offset:2px; }
  .trigger:disabled { opacity:.55; cursor:default; }
  .label { min-width:0; overflow:hidden; text-overflow:ellipsis; }
  .chevron { display:block; width:10px; height:10px; flex:none; opacity:.75; }
  .chevron.up { transform:rotate(180deg); }
  .menu { position:fixed; z-index:190; min-width:132px; width:max-content; max-width:calc(100vw - 16px); max-height:calc(100vh - 16px); overflow:auto; box-sizing:border-box; padding:4px; background:#fff; border:1px solid var(--border, #ecebe6); border-radius:9px; box-shadow:0 12px 32px rgba(30,25,15,.16); visibility:hidden; }
  .menu.ready { visibility:visible; }
  .menu button { width:100%; display:flex; align-items:center; gap:7px; padding:5px 7px; border:0; border-radius:6px; background:none; color:var(--text, #302e28); font:inherit; font-size:11.5px; line-height:1.25; text-align:left; cursor:pointer; }
  .menu button:hover,.menu button:focus-visible { background:#f1efe9; }
  .menu button.selected { background:#efeee9; }
  .menu button span { flex:1; min-width:0; white-space:nowrap; }
  .mode-icon { display:block; width:12px; height:12px; flex:none; color:var(--faint, #a29d93); }
  .check { display:block; width:11px; height:11px; flex:none; }
</style>
