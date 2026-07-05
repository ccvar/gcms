<script lang="ts">
  // 客户端（GCMS Pilot）应用图标：米色圆角方 + 红棕「C」，与 src-tauri/icons 的 app 图标一致。
  // 区别于 SiteMark（红棕底 + 白「C」，代表平台/站点）。
  let { size = 18 }: { size?: number } = $props();
</script>

<svg class="ai" width={size} height={size} viewBox="0 0 64 64" role="img" aria-label="GCMS Pilot">
  <rect width="64" height="64" rx="15" fill="#f1ece1" />
  <path d="M44 24a14 14 0 1 0 0 16" fill="none" stroke="#a03c2b" stroke-width="7" stroke-linecap="round" />
  <circle cx="45.5" cy="42" r="4.6" fill="#a03c2b" />
</svg>

<style>
  .ai { flex: none; border-radius: 5px; display: block; }
</style>
