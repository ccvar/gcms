<script lang="ts" generics="T extends { id: string; updated_at: number }">
  import type { Snippet } from 'svelte';

  let { items, label, row }: { items: T[]; label: string; row: Snippet<[T]> } = $props();
  let expanded = $state(false);
  const limit = 6;
  const ordered = $derived([...items].sort((a, b) => b.updated_at - a.updated_at));
  const visible = $derived(expanded ? ordered : ordered.slice(0, limit));
</script>

{#each visible as conversation (conversation.id)}
  {@render row(conversation)}
{/each}
{#if ordered.length > limit}
  <button
    type="button"
    class="conversation-more"
    aria-expanded={expanded}
    aria-label={expanded ? `收起${label}的更多对话` : `展开${label}的其余 ${ordered.length - limit} 条对话`}
    onclick={() => (expanded = !expanded)}
  >
    <svg class:expanded width="10" height="10" viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="m3 4.5 3 3 3-3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
    <span>{expanded ? '收起' : `展开更多（${ordered.length - limit}）`}</span>
  </button>
{/if}

<style>
  .conversation-more { display:flex; align-items:center; gap:6px; margin:2px 8px 5px; padding:4px 6px 4px 19px; border:0; border-radius:6px; background:transparent; color:var(--dim); font:inherit; font-size:11px; cursor:pointer; }
  .conversation-more:hover { background:var(--soft); color:var(--text); }
  svg { flex:none; transition:transform .12s; }
  svg.expanded { transform:rotate(180deg); }
</style>
