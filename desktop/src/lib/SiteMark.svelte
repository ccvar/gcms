<script lang="ts">
  // 默认站点 / 平台品牌标记（gcms 默认 favicon 的「C」标）。
  let { size = 18 }: { size?: number } = $props();
</script>

<svg class="sm" width={size} height={size} viewBox="0 0 64 64" role="img" aria-label="站点">
  <rect width="64" height="64" rx="15" fill="#9a3b2f" />
  <path d="M44 24a14 14 0 1 0 0 16" fill="none" stroke="#ffffff" stroke-width="7" stroke-linecap="round" />
  <circle cx="45.5" cy="42" r="4.6" fill="#ffffff" />
</svg>

<style>
  .sm { flex: none; border-radius: 5px; display: block; }
</style>
