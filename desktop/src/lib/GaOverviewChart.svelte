<script lang="ts">
  type Metric = 'active_users' | 'sessions';
  type TrendRow = {
    values: string[];
    active_users: number;
    sessions: number;
  };

  let {
    rows = [],
    days = 30,
    loading = false,
    error = '',
    onOpenTrend,
  }: {
    rows?: TrendRow[];
    days?: number;
    loading?: boolean;
    error?: string;
    onOpenTrend?: () => void;
  } = $props();

  const width = 640;
  const height = 112;
  const insetX = 8;
  const insetY = 9;
  let metric = $state<Metric>('active_users');
  let hoverIndex = $state<number | null>(null);

  function rawDate(row: TrendRow): string {
    return row.values?.[0]?.trim() ?? '';
  }

  function sortableDate(value: string): string {
    return value.replace(/\D/g, '').slice(0, 8);
  }

  function displayDate(value: string, full = false): string {
    const raw = sortableDate(value);
    if (raw.length !== 8) return value || '未知日期';
    return full
      ? `${raw.slice(0, 4)}-${raw.slice(4, 6)}-${raw.slice(6, 8)}`
      : `${Number(raw.slice(4, 6))}/${Number(raw.slice(6, 8))}`;
  }

  function compact(value: number): string {
    return new Intl.NumberFormat('zh-CN', {
      notation: value >= 10000 ? 'compact' : 'standard',
      maximumFractionDigits: 1,
    }).format(value);
  }

  const normalizedRows = $derived.by(() =>
    [...rows]
      .filter((row) => rawDate(row))
      .sort((a, b) => sortableDate(rawDate(a)).localeCompare(sortableDate(rawDate(b)))),
  );

  const points = $derived.by(() => {
    const values = normalizedRows.map((row) => Math.max(0, Number(row[metric]) || 0));
    const max = Math.max(1, ...values);
    const span = Math.max(1, values.length - 1);
    return normalizedRows.map((row, index) => {
      const value = values[index];
      return {
        row,
        value,
        x: insetX + (index / span) * (width - insetX * 2),
        y: insetY + (1 - value / max) * (height - insetY * 2),
      };
    });
  });

  const linePath = $derived(points.map((point, index) => `${index ? 'L' : 'M'} ${point.x.toFixed(2)} ${point.y.toFixed(2)}`).join(' '));
  const areaPath = $derived(points.length
    ? `${linePath} L ${points[points.length - 1].x.toFixed(2)} ${height - insetY} L ${points[0].x.toFixed(2)} ${height - insetY} Z`
    : '');
  const maxValue = $derived(Math.max(0, ...points.map((point) => point.value)));
  const averageValue = $derived(points.length
    ? points.reduce((sum, point) => sum + point.value, 0) / points.length
    : 0);
  const activePoint = $derived(hoverIndex == null ? null : points[hoverIndex] ?? null);
  const chartColor = $derived(metric === 'active_users' ? '#f9ab00' : '#34a853');
  const metricLabel = $derived(metric === 'active_users' ? '活跃用户' : '访问次数');

  function moveHover(event: PointerEvent) {
    if (points.length < 2) return;
    const rect = (event.currentTarget as SVGElement).getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (event.clientX - rect.left) / Math.max(1, rect.width)));
    hoverIndex = Math.round(ratio * (points.length - 1));
  }
</script>

<section class="ga-overview-chart" aria-label={`近 ${days} 天 Google Analytics 趋势`}>
  <header>
    <div>
      <b>近 {days} 天趋势</b>
      <small>{points.length ? `${displayDate(rawDate(points[0].row))}–${displayDate(rawDate(points[points.length - 1].row))}` : '按日观察访问变化'}</small>
    </div>
    <div class="chart-actions">
      <div class="metric-switch" aria-label="趋势指标">
        <button type="button" class:active={metric === 'active_users'} onclick={() => { metric = 'active_users'; hoverIndex = null; }}>用户</button>
        <button type="button" class:active={metric === 'sessions'} onclick={() => { metric = 'sessions'; hoverIndex = null; }}>访问</button>
      </div>
      <button type="button" class="open-trend" onclick={() => onOpenTrend?.()}>完整趋势 <span aria-hidden="true">→</span></button>
    </div>
  </header>

  {#if loading && !points.length}
    <div class="chart-loading" aria-label="正在读取每日趋势"><i></i><i></i><i></i></div>
  {:else if error && !points.length}
    <div class="chart-empty error"><b>趋势暂时不可用</b><span>{error}</span></div>
  {:else if points.length >= 2}
    <div class="chart-stage" style={`--chart-color:${chartColor}`}>
      <div class="chart-summary">
        <span><small>日均</small><b>{compact(averageValue)}</b></span>
        <span><small>峰值</small><b>{compact(maxValue)}</b></span>
      </div>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={`${metricLabel}每日趋势`}
        onpointermove={moveHover}
        onpointerleave={() => { hoverIndex = null; }}
      >
        <defs>
          <linearGradient id="ga-overview-area" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color={chartColor} stop-opacity=".2" />
            <stop offset="100%" stop-color={chartColor} stop-opacity=".015" />
          </linearGradient>
        </defs>
        <line class="grid" x1={insetX} y1={insetY} x2={width - insetX} y2={insetY} />
        <line class="grid" x1={insetX} y1={height / 2} x2={width - insetX} y2={height / 2} />
        <line class="grid" x1={insetX} y1={height - insetY} x2={width - insetX} y2={height - insetY} />
        <path class="area" d={areaPath} />
        <path class="line" d={linePath} />
        {#if activePoint}
          <line class="cursor" x1={activePoint.x} y1={insetY} x2={activePoint.x} y2={height - insetY} />
          <circle cx={activePoint.x} cy={activePoint.y} r="4" />
        {/if}
      </svg>
      <div class="chart-dates" aria-hidden="true">
        <span>{displayDate(rawDate(points[0].row))}</span>
        <span>{displayDate(rawDate(points[points.length - 1].row))}</span>
      </div>
      {#if activePoint}
        <div
          class="chart-tooltip"
          class:right={activePoint.x > width * .62}
          style={`left:${(activePoint.x / width) * 100}%`}
        >
          <small>{displayDate(rawDate(activePoint.row), true)}</small>
          <b>{metricLabel} {compact(activePoint.value)}</b>
        </div>
      {/if}
    </div>
  {:else}
    <div class="chart-empty"><b>暂时没有连续趋势</b><span>至少有两天数据后会自动绘制。</span></div>
  {/if}
</section>

<style>
  .ga-overview-chart {
    display: grid;
    gap: 7px;
    margin-top: 9px;
    padding: 9px 10px 8px;
    border: 1px solid #ebe8e0;
    border-radius: 10px;
    background: #fff;
  }
  header { min-width: 0; display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  header > div:first-child { min-width: 0; display: grid; gap: 1px; }
  header b { font-size: 11px; line-height: 1.2; }
  header small { overflow: hidden; color: #a19c92; font-size: 8.5px; text-overflow: ellipsis; white-space: nowrap; }
  .chart-actions { flex: none; display: inline-flex; align-items: center; gap: 7px; }
  .metric-switch { display: inline-flex; gap: 1px; padding: 2px; border-radius: 7px; background: #f0eee8; }
  .metric-switch button, .open-trend {
    border: 0;
    background: transparent;
    color: #777269;
    font: inherit;
    cursor: pointer;
  }
  .metric-switch button { min-width: 34px; height: 22px; padding: 0 6px; border-radius: 5px; font-size: 9px; font-weight: 650; }
  .metric-switch button.active { background: #fff; color: #292722; box-shadow: 0 1px 3px rgb(36 31 23 / 9%); }
  .open-trend { display: inline-flex; align-items: center; gap: 2px; padding: 2px 0; font-size: 8.5px; font-weight: 650; }
  .open-trend:hover { color: #292722; }
  .chart-stage { position: relative; min-width: 0; }
  .chart-stage svg { width: 100%; height: 80px; display: block; overflow: visible; color: var(--chart-color); touch-action: none; }
  .grid { stroke: #eeece6; stroke-width: 1; vector-effect: non-scaling-stroke; }
  .area { fill: url(#ga-overview-area); }
  .line { fill: none; stroke: var(--chart-color); stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; vector-effect: non-scaling-stroke; }
  .cursor { stroke: #bdb8ad; stroke-width: 1; stroke-dasharray: 2 3; vector-effect: non-scaling-stroke; }
  circle { fill: #fff; stroke: var(--chart-color); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
  .chart-summary { position: absolute; z-index: 2; top: 4px; left: 9px; display: inline-flex; gap: 9px; pointer-events: none; }
  .chart-summary span { display: inline-flex; align-items: baseline; gap: 3px; }
  .chart-summary small { color: #a19c92; font-size: 7.5px; }
  .chart-summary b { color: #4c4942; font-size: 9px; font-variant-numeric: tabular-nums; }
  .chart-dates { display: flex; justify-content: space-between; margin-top: -3px; color: #aaa59b; font-size: 7.5px; font-variant-numeric: tabular-nums; }
  .chart-tooltip {
    position: absolute;
    z-index: 3;
    top: 9px;
    display: grid;
    gap: 1px;
    min-width: 82px;
    padding: 5px 7px;
    border-radius: 7px;
    background: #2e2b27;
    color: #fff;
    box-shadow: 0 5px 14px rgb(28 24 18 / 16%);
    pointer-events: none;
    transform: translateX(8px);
  }
  .chart-tooltip.right { transform: translateX(calc(-100% - 8px)); }
  .chart-tooltip small { color: #c9c5bd; font-size: 7.5px; }
  .chart-tooltip b { font-size: 9px; font-variant-numeric: tabular-nums; }
  .chart-loading { height: 78px; display: flex; align-items: flex-end; gap: 4px; padding: 12px 5px 4px; }
  .chart-loading i { flex: 1; border-radius: 5px; background: linear-gradient(100deg, #efede7 20%, #f8f7f4 42%, #efede7 64%); background-size: 220% 100%; animation: shimmer 1.25s linear infinite; }
  .chart-loading i:nth-child(1) { height: 42%; }
  .chart-loading i:nth-child(2) { height: 72%; }
  .chart-loading i:nth-child(3) { height: 55%; }
  .chart-empty { min-height: 58px; display: flex; align-items: center; justify-content: center; gap: 5px; border: 1px dashed #dedad1; border-radius: 8px; color: #a19c92; font-size: 8.5px; text-align: center; }
  .chart-empty b { color: #625e56; font-size: 9.5px; }
  .chart-empty.error { color: #b34a3d; }
  .chart-empty.error span { max-width: 62%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @keyframes shimmer { to { background-position-x: -220%; } }
  @media (max-width: 520px) {
    .open-trend { display: none; }
    .chart-stage svg { height: 72px; }
  }
  @media (prefers-reduced-motion: reduce) {
    .chart-loading i { animation: none; }
  }
</style>
