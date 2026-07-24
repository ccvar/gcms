<script lang="ts">
  type TrendRow = {
    date: string;
    clicks: number;
    impressions: number;
  };

  type Point = {
    row: TrendRow;
    x: number;
    clicksY: number;
    impressionsY: number;
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
  const height = 104;
  const insetX = 8;
  const insetY = 9;
  let hoverIndex = $state<number | null>(null);

  function finite(value: unknown): number {
    const number = Number(value);
    return Number.isFinite(number) ? Math.max(0, number) : 0;
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
    rows
      .filter((row) => row?.date?.trim())
      .map((row) => ({
        date: row.date.trim(),
        clicks: finite(row.clicks),
        impressions: finite(row.impressions),
      }))
      .sort((a, b) => sortableDate(a.date).localeCompare(sortableDate(b.date))),
  );

  const clicksTotal = $derived(normalizedRows.reduce((sum, row) => sum + row.clicks, 0));
  const impressionsTotal = $derived(normalizedRows.reduce((sum, row) => sum + row.impressions, 0));
  const clicksMax = $derived(Math.max(1, ...normalizedRows.map((row) => row.clicks)));
  const impressionsMax = $derived(Math.max(1, ...normalizedRows.map((row) => row.impressions)));

  const points = $derived.by(() => {
    const span = Math.max(1, normalizedRows.length - 1);
    return normalizedRows.map((row, index): Point => ({
      row,
      x: insetX + (index / span) * (width - insetX * 2),
      clicksY: yFor(row.clicks, clicksMax),
      impressionsY: yFor(row.impressions, impressionsMax),
    }));
  });

  const clicksPath = $derived(pathFor(points, 'clicksY'));
  const impressionsPath = $derived(pathFor(points, 'impressionsY'));
  const impressionsArea = $derived(points.length
    ? `${impressionsPath} L ${points[points.length - 1].x.toFixed(2)} ${height - insetY} L ${points[0].x.toFixed(2)} ${height - insetY} Z`
    : '');
  const activePoint = $derived(hoverIndex == null ? null : points[hoverIndex] ?? null);

  function yFor(value: number, max: number): number {
    return insetY + (1 - value / Math.max(1, max)) * (height - insetY * 2);
  }

  function pathFor(values: Point[], key: 'clicksY' | 'impressionsY'): string {
    return values
      .map((point, index) => `${index ? 'L' : 'M'} ${point.x.toFixed(2)} ${point[key].toFixed(2)}`)
      .join(' ');
  }

  function moveHover(event: PointerEvent) {
    if (points.length < 2) return;
    const rect = (event.currentTarget as SVGElement).getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (event.clientX - rect.left) / Math.max(1, rect.width)));
    hoverIndex = Math.round(ratio * (points.length - 1));
  }
</script>

<section class="gsc-overview-chart" aria-label={`近 ${days} 天 Google 搜索点击与曝光趋势`}>
  <header>
    <div>
      <b>近 {days} 天搜索趋势</b>
      <small>{points.length ? `${displayDate(points[0].row.date)}–${displayDate(points[points.length - 1].row.date)}` : '同时观察点击与曝光变化'}</small>
    </div>
    {#if onOpenTrend}
      <button type="button" class="open-trend" onclick={onOpenTrend}>完整趋势 <span aria-hidden="true">→</span></button>
    {/if}
  </header>

  <div class="chart-legend" aria-label="图表图例">
    <span class="clicks"><i></i>点击 <b>{compact(clicksTotal)}</b></span>
    <span class="impressions"><i></i>曝光 <b>{compact(impressionsTotal)}</b></span>
    <small data-tip="点击与曝光量级不同，图中分别按各自峰值绘制，以便同时观察变化方向。">双尺度</small>
  </div>

  {#if loading && points.length < 2}
    <div class="chart-loading" aria-label="正在读取每日搜索趋势"><i></i><i></i><i></i></div>
  {:else if error && points.length < 2}
    <div class="chart-empty error"><b>趋势暂时不可用</b><span>{error}</span></div>
  {:else if points.length >= 2}
    <div class="chart-stage">
      <svg
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={`搜索点击 ${clicksTotal}，曝光 ${impressionsTotal}`}
        onpointermove={moveHover}
        onpointerleave={() => { hoverIndex = null; }}
      >
        <defs>
          <linearGradient id="gsc-overview-impressions-area" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="#7651c9" stop-opacity=".16" />
            <stop offset="100%" stop-color="#7651c9" stop-opacity=".012" />
          </linearGradient>
        </defs>
        <line class="grid" x1={insetX} y1={insetY} x2={width - insetX} y2={insetY} />
        <line class="grid" x1={insetX} y1={height / 2} x2={width - insetX} y2={height / 2} />
        <line class="grid" x1={insetX} y1={height - insetY} x2={width - insetX} y2={height - insetY} />
        <path class="impressions-area" d={impressionsArea} />
        <path class="series impressions" d={impressionsPath} />
        <path class="series clicks" d={clicksPath} />
        {#if activePoint}
          <line class="cursor" x1={activePoint.x} y1={insetY} x2={activePoint.x} y2={height - insetY} />
          <circle class="impressions-point" cx={activePoint.x} cy={activePoint.impressionsY} r="3.5" />
          <circle class="clicks-point" cx={activePoint.x} cy={activePoint.clicksY} r="3.5" />
        {/if}
      </svg>
      <div class="chart-dates" aria-hidden="true">
        <span>{displayDate(points[0].row.date)}</span>
        <span>{displayDate(points[points.length - 1].row.date)}</span>
      </div>
      {#if activePoint}
        <div
          class="chart-tooltip"
          class:right={activePoint.x > width * .62}
          style={`left:${(activePoint.x / width) * 100}%`}
        >
          <small>{displayDate(activePoint.row.date, true)}</small>
          <span class="clicks"><i></i>点击 <b>{compact(activePoint.row.clicks)}</b></span>
          <span class="impressions"><i></i>曝光 <b>{compact(activePoint.row.impressions)}</b></span>
        </div>
      {/if}
    </div>
  {:else}
    <div class="chart-empty"><b>暂时没有连续趋势</b><span>至少有两天数据后会自动绘制。</span></div>
  {/if}
</section>

<style>
  .gsc-overview-chart {
    display: grid;
    gap: 6px;
    margin-top: 9px;
    padding: 9px 10px 8px;
    border: 1px solid #ebe8e0;
    border-radius: 10px;
    background: #fff;
    color: #292722;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    min-width: 0;
  }
  header > div {
    display: grid;
    gap: 1px;
    min-width: 0;
  }
  header b { font-size: 11px; line-height: 1.2; }
  header small {
    overflow: hidden;
    color: #a19c92;
    font-size: 8.5px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .open-trend {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 2px;
    padding: 2px 0;
    border: 0;
    background: transparent;
    color: #6e6a62;
    font: inherit;
    font-size: 8.5px;
    font-weight: 650;
    cursor: pointer;
  }
  .open-trend:hover { color: #292722; }
  .chart-legend {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    color: #777269;
    font-size: 8.5px;
  }
  .chart-legend span,
  .chart-tooltip span {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .chart-legend i,
  .chart-tooltip i {
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
  .chart-legend .clicks i,
  .chart-tooltip .clicks i { background: #2f6fed; }
  .chart-legend .impressions i,
  .chart-tooltip .impressions i { background: #7651c9; }
  .chart-legend b { color: #4c4942; font-size: 9px; font-variant-numeric: tabular-nums; }
  .chart-legend > small {
    margin-left: auto;
    color: #aaa59b;
    font-size: 7.5px;
    cursor: help;
  }
  .chart-stage { position: relative; min-width: 0; }
  .chart-stage svg {
    display: block;
    width: 100%;
    height: 80px;
    overflow: visible;
    touch-action: none;
  }
  .grid { stroke: #eeece6; stroke-width: 1; vector-effect: non-scaling-stroke; }
  .impressions-area { fill: url(#gsc-overview-impressions-area); }
  .series {
    fill: none;
    stroke-width: 1.4;
    stroke-linecap: round;
    stroke-linejoin: round;
    vector-effect: non-scaling-stroke;
  }
  .series.clicks { stroke: #2f6fed; }
  .series.impressions { stroke: #7651c9; opacity: .88; }
  .cursor {
    stroke: #bdb8ad;
    stroke-width: 1;
    stroke-dasharray: 2 3;
    vector-effect: non-scaling-stroke;
  }
  circle { fill: #fff; stroke-width: 1.4; vector-effect: non-scaling-stroke; }
  .clicks-point { stroke: #2f6fed; }
  .impressions-point { stroke: #7651c9; }
  .chart-dates {
    display: flex;
    justify-content: space-between;
    margin-top: -3px;
    color: #aaa59b;
    font-size: 7.5px;
    font-variant-numeric: tabular-nums;
  }
  .chart-tooltip {
    position: absolute;
    z-index: 3;
    top: 4px;
    display: grid;
    gap: 3px;
    min-width: 90px;
    padding: 5px 7px;
    border-radius: 7px;
    background: #2e2b27;
    color: #fff;
    box-shadow: 0 5px 14px rgb(28 24 18 / 16%);
    font-size: 8.5px;
    pointer-events: none;
    transform: translateX(8px);
  }
  .chart-tooltip.right { transform: translateX(calc(-100% - 8px)); }
  .chart-tooltip small { color: #c9c5bd; font-size: 7.5px; }
  .chart-tooltip b { margin-left: auto; color: #fff; font-size: 9px; font-variant-numeric: tabular-nums; }
  .chart-loading {
    display: flex;
    align-items: flex-end;
    gap: 4px;
    height: 76px;
    padding: 10px 5px 3px;
  }
  .chart-loading i {
    flex: 1;
    border-radius: 4px;
    background: linear-gradient(100deg, #efede7 20%, #f8f7f4 42%, #efede7 64%);
    background-size: 220% 100%;
    animation: shimmer 1.25s linear infinite;
  }
  .chart-loading i:nth-child(1) { height: 38%; }
  .chart-loading i:nth-child(2) { height: 72%; }
  .chart-loading i:nth-child(3) { height: 52%; }
  .chart-empty {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 5px;
    min-height: 58px;
    border: 1px dashed #dedad1;
    border-radius: 8px;
    color: #a19c92;
    font-size: 8.5px;
    text-align: center;
  }
  .chart-empty b { color: #625e56; font-size: 9.5px; }
  .chart-empty.error { color: #b34a3d; }
  .chart-empty.error span {
    max-width: 62%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  @keyframes shimmer { to { background-position-x: -220%; } }
  @media (max-width: 520px) {
    .open-trend { display: none; }
    .chart-stage svg { height: 72px; }
  }
  @media (prefers-reduced-motion: reduce) {
    .chart-loading i { animation: none; }
  }
</style>
