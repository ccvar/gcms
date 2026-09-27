<script lang="ts">
  import SiteFav from './SiteFav.svelte';
  let { options, value, onchange }: {options:{value:string;label:string;sub?:string;img?:string;disabled?:boolean}[];value:string[];onchange:(value:string[])=>void} = $props();
  let query = $state('');
  const filtered = $derived(options.filter(o => `${o.label} ${o.value} ${o.sub || ''}`.toLowerCase().includes(query.trim().toLowerCase())));
  const available = $derived(filtered.filter(o=>!o.disabled));
  const all = $derived(available.length>0 && available.every(o=>value.includes(o.value)));
  function toggle(slug:string) { onchange(value.includes(slug)?value.filter(v=>v!==slug):[...value,slug]); }
</script>
<div class="picker">
  <div class="tools"><input aria-label="搜索可托管站点" placeholder="搜索站点名称或域名…" bind:value={query} /><span>已选 {value.length} 个</span></div>
  <label class="all"><input type="checkbox" checked={all} disabled={!available.length} onchange={()=>onchange(all?value.filter(v=>!available.some(o=>o.value===v)):[...new Set([...value,...available.map(o=>o.value)])])} />全选{query ? '搜索结果' : '可用站点'}</label>
  <div class="list">
    {#each filtered as o (o.value)}
      <label class:disabled={o.disabled} class:selected={value.includes(o.value)}><input type="checkbox" disabled={o.disabled} checked={value.includes(o.value)} onchange={()=>toggle(o.value)} /><SiteFav src={o.img || ''} label={o.label} size={22} /><span><b>{o.label}</b><small>{o.sub}</small></span></label>
    {/each}
    {#if !filtered.length}<p>没有匹配的站点</p>{/if}
  </div>
</div>
<style>
  .picker{border:1px solid var(--border2);border-radius:12px;overflow:hidden}.tools{display:flex;align-items:center;gap:12px;padding:10px 12px}.tools input{flex:1;min-width:0;padding:9px 10px;background:var(--rail);border:0;border-radius:7px;color:var(--text);font:inherit;font-size:13px}.tools span{font-size:12px;color:var(--dim);white-space:nowrap}.all{border-top:1px solid var(--border);border-bottom:1px solid var(--border);font-size:12px;color:var(--dim)}label{display:flex;align-items:center;gap:12px;padding:10px 14px;cursor:pointer}input[type=checkbox]{accent-color:var(--accent);flex:none}.list{max-height:260px;overflow:auto}.list label:hover,.selected{background:var(--rail)}.list span{display:flex;flex-direction:column;gap:3px;min-width:0}.list b{font-size:13px;font-weight:600}.list small{font-size:11px;color:var(--dim)}.disabled{opacity:.45;cursor:default}p{padding:12px;color:var(--dim);font-size:13px}
</style>
