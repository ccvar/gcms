<script lang="ts">
  let { domain = '', size = 14 }: { domain?: string; size?: number } = $props();
  let sourceIndex = $state(0);
  const normalizedDomain = $derived(domain.trim().toLowerCase());
  const sources = $derived(normalizedDomain ? [
    `https://${normalizedDomain}/favicon.ico`,
    `https://${normalizedDomain}/favicon.svg`,
    `https://${normalizedDomain}/favicon.png`,
    `https://${normalizedDomain}/brand/favicon-64.png`,
    `https://${normalizedDomain}/apple-touch-icon.png`,
  ] : []);
  const src = $derived(sources[sourceIndex] || '');

  $effect(() => {
    void normalizedDomain;
    sourceIndex = 0;
  });
</script>

<span class="skill-fav" style={`width:${size}px;height:${size}px`} aria-hidden="true">
  {#if src}
    <img src={src} alt="" loading="lazy" onerror={() => (sourceIndex += 1)} />
  {:else}
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
      <path d="m2.5 5 5.5-3 5.5 3v6L8 14l-5.5-3V5Z" stroke="currentColor" stroke-width="1.25" stroke-linejoin="round" />
      <path d="M2.8 5 8 8l5.2-3M8 8v6" stroke="currentColor" stroke-width="1.25" stroke-linejoin="round" />
    </svg>
  {/if}
</span>

<style>
  .skill-fav { flex: none; display: inline-grid; place-items: center; color: currentColor; }
  .skill-fav img { width: 100%; height: 100%; border-radius: 3px; object-fit: contain; background: transparent; }
</style>
