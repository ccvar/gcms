<script lang="ts">
  import { tick } from 'svelte';
  import SettingsIcon from './SettingsIcon.svelte';
  let { onedit, ondelete, ontoggle, paused }: {onedit:()=>void;ondelete:()=>void;ontoggle:()=>void;paused:boolean} = $props();
  let open = $state(false), ready = $state(false);
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();
  let menu = $state<HTMLDivElement>();
  let first = $state<HTMLButtonElement>();
  let style = $state('');
  async function toggle() {
    if (open) { open=false; return; }
    if (!trigger) return;
    ready=false; open=true;
    await tick();
    if (!open || !menu || !trigger) return;
    const r=trigger.getBoundingClientRect(), m=menu.getBoundingClientRect();
    const x=Math.max(8,Math.min(window.innerWidth-m.width-8,r.right-m.width));
    const y=Math.max(8,Math.min(window.innerHeight-m.height-8,window.innerHeight-r.bottom<m.height+13?r.top-m.height-5:r.bottom+5));
    style=`left:${x}px;top:${y}px`;
    ready=true;
    await tick();
    first?.focus({preventScroll:true});
  }
  function key(e:KeyboardEvent) {
    if (!open) return;
    if(e.key==='Escape'){e.preventDefault();open=false;trigger?.focus();}
    else if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();const items=Array.from(root?.querySelectorAll<HTMLButtonElement>('[role=menuitem]')||[]);items[(items.indexOf(document.activeElement as HTMLButtonElement)+(e.key==='ArrowDown'?1:items.length-1))%items.length]?.focus();}
    else if(e.key==='Tab')open=false;
  }
  $effect(()=>{
    if (!open) return;
    const closeOnScroll=(e:Event)=>{if (!menu?.contains(e.target as Node))open=false;};
    window.addEventListener('scroll',closeOnScroll,true);
    return ()=>window.removeEventListener('scroll',closeOnScroll,true);
  });
</script>
<svelte:window onpointerdown={e=>{if(root&&!root.contains(e.target as Node))open=false;}} onkeydown={key} onresize={()=>open=false} />
<div bind:this={root}>
  <button class="trigger" class:active={open} bind:this={trigger} aria-label="托管设置" title="托管设置" aria-haspopup="menu" aria-expanded={open} onclick={toggle}><SettingsIcon /></button>
  {#if open}
    <div class="menu" class:ready bind:this={menu} role="menu" aria-label="托管操作" {style}>
      <button bind:this={first} role="menuitem" onclick={()=>{open=false;onedit();}}>
        <svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M11.5 2.5l2 2L6 12l-2.5.5L4 10l7.5-7.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" /></svg><span>编辑托管</span>
      </button>
      <button role="menuitem" onclick={()=>{open=false;ontoggle();}}>
        <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">{#if paused}<path d="M5 3.4l7.2 4.6L5 12.6z" fill="currentColor" />{:else}<path d="M5.5 3.5v9M10.5 3.5v9" stroke="currentColor" stroke-width="2" stroke-linecap="round" />{/if}</svg><span>{paused?'恢复托管':'暂停托管'}</span>
      </button>
      <div class="divider" aria-hidden="true"></div>
      <button class="danger" role="menuitem" onclick={()=>{open=false;ondelete();}}>
        <svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M3.4 4.5h9.2M6 2.5h4l.6 2M5 6.5v6M8 6.5v6M11 6.5v6M4.2 4.5l.6 9h6.4l.6-9" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" /></svg><span>删除托管</span>
      </button>
    </div>
  {/if}
</div>
<style>
  .trigger { background:none; border:none; cursor:pointer; padding:6px; border-radius:8px; color:var(--dim); display:inline-flex; align-items:center; justify-content:center; }
  .trigger:hover { background:#f1efe9; color:var(--text); }
  .trigger.active { background:#ebe8e1; color:var(--text); }
  .menu { position:fixed; z-index:190; width:max-content; min-width:112px; max-width:min(180px,calc(100vw - 16px)); max-height:calc(100vh - 16px); overflow-x:hidden; overflow-y:auto; background:#fff; border:1px solid var(--border); border-radius:9px; box-shadow:0 12px 32px rgba(30,25,15,.16); padding:4px; visibility:hidden; }
  .menu.ready { visibility:visible; }
  .menu button { width:100%; display:flex; align-items:center; justify-content:flex-start; gap:7px; background:none; border:none; border-radius:6px; padding:5px 7px; font:inherit; font-size:11.5px; line-height:1.25; color:var(--text); cursor:pointer; text-align:left; }
  .menu button:hover,.menu button:focus-visible { background:#f1efe9; }
  .menu button svg { display:block; width:12px; height:12px; flex:none; color:var(--faint); }
  .menu button span { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .menu .danger,.menu .danger svg { color:var(--err); }
  .menu .danger:hover,.menu .danger:focus-visible { background:var(--err-soft); }
  .divider { height:1px; margin:3px 5px; background:var(--border); }
</style>
