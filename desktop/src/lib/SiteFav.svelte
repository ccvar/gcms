<script lang="ts">
  // 站点小图标：有 favicon/logo 用图，加载失败或没有则用 slug 首字母圆形占位。
  let { src = '', label = '', size = 14 }: { src?: string; label?: string; size?: number } = $props();
  let broken = $state(false);
  $effect(() => { void src; broken = false; }); // src 变化时重置失败态
</script>

{#if src && !broken}
  <img class="sf" style="width:{size}px;height:{size}px" src={src} alt="" loading="lazy" onerror={() => (broken = true)} />
{:else}
  <span class="sf ph" style="width:{size}px;height:{size}px;font-size:{Math.round(size * 0.58)}px">{(label || '?').slice(0, 1).toUpperCase()}</span>
{/if}

<style>
  /* 有真实图标时不垫底色；底色只留给字母占位（.ph） */
  .sf { flex: none; border-radius: 4px; object-fit: contain; background: transparent; vertical-align: -0.15em; }
  .sf.ph { display: inline-flex; align-items: center; justify-content: center; font-weight: 600; color: var(--dim, #6f6b62); border: none; background: #e7e4dd; }
</style>
